import pkg from "mineflayer-pathfinder";
import mineflayer from "mineflayer";
import { mineflayer as viewer } from "prismarine-viewer";
import { plugin as collectBlock } from "mineflayer-collectblock";
import * as config from "./lib/config.js";
import { log } from "./lib/logger.js";
import * as models from "./lib/models.js";
import { runTask } from "./lib/tasks/task.js";
import { SynapticClient } from "./lib/network/client.js";
import { SurvivalSystem } from "./lib/systems/survival.js";
import { getThreats, computeSafeRetreat } from "./lib/utils/threats.js";
import { getPOIs } from "./lib/utils/perception.js";
import { Vec3 } from "vec3";
const { pathfinder, Movements, goals } = pkg;

let viewerStarted = false;
function isValidPosition(pos: any): boolean {
    if (!pos) return false;
    const { x, y, z } = pos;
    return (
        typeof x === "number" &&
        typeof y === "number" &&
        typeof z === "number" &&
        Math.abs(x) < 1e6 &&
        Math.abs(y) < 1e6 &&
        Math.abs(z) < 1e6 &&
        !isNaN(x) &&
        !isNaN(y) &&
        !isNaN(z)
    );
}

const isIgnorableError = (err: Error | any): boolean => {
    if (!err) return false;
    const msg = String(err?.message || err?.stack || err);
    const name = String(err?.name || "");
    const code = String(err?.code || "");
    return (
        name.includes("PartialReadError") ||
        msg.includes("Read error for undefined") ||
        msg.includes("Missing characters in string") ||
        msg.includes("protodef") ||
        (msg.includes("size is") && msg.includes("expected size")) ||
        msg.includes("Unexpected server response") ||
        code === "ECONNRESET" ||
        code === "ECONNREFUSED" ||
        code === "EADDRINUSE" ||
        msg.includes("ECONNRESET") ||
        msg.includes("ECONNREFUSED") ||
        msg.includes("EADDRINUSE") ||
        msg.includes("socket hang up")
    );
};

const originalConsoleError = console.error;
console.error = (...args: any[]) => {
    const firstArg = args[0];
    const msg =
        typeof firstArg === "object"
            ? String(firstArg?.err?.message || firstArg?.message || "")
            : String(firstArg);
    if (
        isIgnorableError(firstArg) ||
        msg.includes("PartialReadError") ||
        msg.includes("protodef")
    )
        return;
    originalConsoleError.apply(console, args);
};

process.on("uncaughtException", (err: Error | any) => {
    if (isIgnorableError(err)) return;
    log.error("Fatal uncaught exception", {
        err: err?.message || String(err),
        stack: err?.stack || "No stack trace available",
    });
    process.exit(1);
});
process.on("unhandledRejection", (reason: any) => {
    if (isIgnorableError(reason)) return;
    log.error("Unhandled promise rejection", { reason });
});

let currentTask: models.ActiveTask | null = null;
let taskAbortController: AbortController | null = null;
let bot: mineflayer.Bot;
let client: SynapticClient;
let survival: SurvivalSystem;
let runtimeConfig: config.Config;
let lastDeathMessage: string = "unknown causes";
let lastStateSig = "";
let lastStatePushTime = 0;
let reconnectAttempt = 1;
let stateInterval: NodeJS.Timeout | null = null;
let isShuttingDown = false;
let isBotSpawned = false;
let hasDied = false; // Track if bot has died to detect respawns
const knownChests: Record<string, { name: string; count: number }[]> = {};
let lastVelocity = { x: 0, y: 0, z: 0 };

let isPathfinding = false;
let pathProgress = 0.0;
function stopMovement() {
    if (!bot || !bot.entity) return;
    try {
        bot.clearControlStates();
        if (bot.pathfinder) {
            bot.pathfinder.setGoal(null);
        }
    } catch (e) {}
}

function completeTask(
    task: models.ActiveTask | null,
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
    cause: string = "",
) {
    if (!task || !client) return;
    if (currentTask?.id === task.id) currentTask = null;
    client.sendEvent(
        status,
        `${task.action} ${task.target?.name || "none"}`,
        task.id,
        cause,
        task.startTime,
    );
}

function abortActiveTask(reason: string) {
    if (currentTask && taskAbortController) {
        taskAbortController.abort(reason);
        stopMovement();
        completeTask(currentTask, "task_aborted", reason);
        currentTask = null;
        taskAbortController = null;
    }
}

function normalizeFailureCause(err: any): string {
    const msg = String(err?.message || err).toLowerCase();
    if (
        msg.includes("no path") ||
        msg.includes("path_fail") ||
        (msg.includes("failed_to_reach") && !msg.includes("furnace")) ||
        msg.includes("pathfinder_timeout") ||
        msg.includes("collect_failed")
    )
        return "PATH_FAILED";
    if (
        msg.includes("timeout") ||
        msg.includes("fsm_global_timeout") ||
        msg.includes("furnace_timeout_or_lag")
    )
        return "TIMEOUT";
    if (msg.includes("stuck")) return "STUCK";
    if (
        msg.includes("no_entity") ||
        msg.includes("missing_entity") ||
        msg.includes("target_lost") ||
        msg.includes("no_interactable_found")
    )
        return "NO_ENTITY";
    if (msg.includes("no_pickaxe_equipped") || msg.includes("no_tool"))
        return "NO_TOOL";
    if (
        msg.includes("no_furnace_available") ||
        msg.includes("failed_to_reach_furnace") ||
        msg.includes("no_chest")
    )
        return "NO_FURNACE";
    if (msg.includes("no_mature_")) return "NO_MATURE_CROP";
    if (
        msg.includes("missing_ingredients") ||
        msg.includes("missing_crafting_table") ||
        msg.includes("craft_action_failed") ||
        msg.includes("not_in_inventory")
    )
        return "MISSING_INGREDIENTS";
    if (
        msg.includes("exhausted") ||
        msg.includes("no_") ||
        msg.includes("missing_") ||
        msg.includes("unknown_")
    )
        return "NO_BLOCKS";
    return "UNKNOWN";
}

async function executeAntiStuckReflex() {
    if (!bot || !bot.entity || bot.health <= 0) return;
    try {
        // First try: Jump and look around
        bot.setControlState("jump", true);
        await new Promise((r) => setTimeout(r, 200));
        bot.setControlState("jump", false);

        const pos = bot.entity.position.floored();
        const blockBelow = bot.blockAt(pos.offset(0, -1, 0));

        // Check if there's a block directly in front of legs
        const yaw = bot.entity.yaw;
        const dx = -Math.sin(yaw);
        const dz = -Math.cos(yaw);
        const frontBlock = bot.blockAt(
            pos.offset(Math.round(dx), 0, Math.round(dz)),
        );

        if (frontBlock && frontBlock.boundingBox === "block") {
            log.info("Anti-stuck: clearing block in front of legs");
            await bot.dig(frontBlock);
            return;
        }

        if (
            blockBelow &&
            blockBelow.name !== "air" &&
            blockBelow.name !== "bedrock" &&
            !["water", "lava"].includes(blockBelow.name)
        ) {
            log.info("Anti-stuck reflex: dismantling block below as fallback");
            const tool = bot.pathfinder.bestHarvestTool(blockBelow);
            if (tool) await bot.equip(tool.type, "hand");
            await bot.dig(blockBelow);
        }
    } catch (e) {
        log.debug("Anti-stuck reflex failed", { err: e });
    }
}

async function executeIntent(intent: models.ActionIntent) {
    try {
        if (!intent?.action || isShuttingDown) return;
        if (!bot || !bot.entity || bot.health <= 0 || !isBotSpawned) {
            log.warn("Task execution blocked: invalid bot state", {
                action: intent.action,
                hasBot: !!bot,
                hasEntity: !!bot?.entity,
                health: bot?.health,
                isSpawned: isBotSpawned,
            });
            client.sendEvent(
                "task_failed",
                `${intent.action} ${intent.target?.name || "none"}`,
                intent.id,
                "INVALID_BOT_STATE",
                Date.now(),
            );
            return;
        }

        if (survival?.isPanickingNow()) {
            if (intent.action === "retreat") {
                survival.reset();
            } else {
                log.info(
                    "Task execution blocked: survival system is panicking",
                    {
                        action: intent.action,
                    },
                );
                client.sendEvent(
                    "task_aborted",
                    "lock",
                    intent.id,
                    "panic",
                    Date.now(),
                );
                return;
            }
        }

        abortActiveTask("preempted");
        taskAbortController = new AbortController();
        const signal = taskAbortController.signal;
        const localController = taskAbortController;
        currentTask = {
            id: intent.id,
            action: intent.action,
            target: intent.target || { type: "none", name: "none" },
            count: intent.count || 1,
            startTime: Date.now(),
            trace: intent.trace || {
                trace_id: "unknown",
                action_id: intent.id,
            },
        };
        isPathfinding = false;
        pathProgress = 0.0;

        client.sendEvent(
            "task_start",
            `${intent.action} ${intent.target?.name || "none"}`,
            intent.id,
            "",
            Date.now(),
        );
        const activeTask = currentTask;
        let timeoutId: NodeJS.Timeout | undefined;

        const highStakes = [
            "hunt",
            "combat",
            "mine",
            "build",
            "retreat",
            "explore",
        ];
        if (highStakes.includes(intent.action)) {
            const jitterMs = 50 + Math.random() * 100;
            await new Promise((r) => setTimeout(r, jitterMs));
        }

        if (intent.action === "explore") {
            log.info("Executing native curiosity exploration");
            if (!isValidPosition(bot.entity.position)) {
                log.error("Invalid position during explore, aborting");
                completeTask(activeTask, "task_failed", "INVALID_POSITION");
                return;
            }
            const goalX = Math.round(
                bot.entity.position.x + (Math.random() - 0.5) * 60,
            );
            const goalZ = Math.round(
                bot.entity.position.z + (Math.random() - 0.5) * 60,
            );
            try {
                const timeouts =
                    runtimeConfig.task_timeouts || config.TASK_TIMEOUTS;
                const timeoutMs = timeouts["explore"] || 30000;
                await Promise.race([
                    bot.pathfinder.goto(new goals.GoalXZ(goalX, goalZ)),
                    new Promise((_, r) => {
                        timeoutId = setTimeout(() => {
                            localController.abort("timeout");
                            stopMovement();
                            r(new Error("timeout"));
                        }, timeoutMs);
                    }),
                    new Promise((_, r) => {
                        signal.addEventListener("abort", () => {
                            stopMovement();
                            r(new Error("aborted"));
                        });
                    }),
                ]);
                if (!signal.aborted && activeTask) {
                    pushState();
                    completeTask(activeTask, "task_completed");
                }
            } catch (err: any) {
                if (!signal.aborted || err.message === "timeout") {
                    const normalizedCause = normalizeFailureCause(err);
                    log.error("Task failed, evaluating reflex", {
                        cause: normalizedCause,
                    });

                    if (
                        normalizedCause === "STUCK" ||
                        normalizedCause === "PATH_FAILED"
                    ) {
                        await executeAntiStuckReflex();
                        // MANDATORY: Wait for physics to settle and broadcast new state
                        await new Promise((r) => setTimeout(r, 500));
                        pushState();
                    }

                    if (activeTask) {
                        completeTask(
                            activeTask,
                            "task_failed",
                            normalizedCause,
                        );
                    }
                }
            } finally {
                if (timeoutId) clearTimeout(timeoutId);
                if (currentTask?.id === activeTask?.id) currentTask = null;
                if (taskAbortController === localController)
                    taskAbortController = null;
            }
            return;
        }

        try {
            const timeouts =
                runtimeConfig.task_timeouts || config.TASK_TIMEOUTS;
            const timeoutMs = timeouts[intent.action] || 30000;
            await Promise.race([
                runTask(
                    bot,
                    intent,
                    signal,
                    timeouts,
                    () => getThreats(bot),
                    (t) => computeSafeRetreat(bot, t, 20),
                    stopMovement,
                ),
                new Promise((_, r) => {
                    timeoutId = setTimeout(() => {
                        localController.abort("timeout");
                        stopMovement();
                        r(new Error("timeout"));
                    }, timeoutMs);
                }),
            ]);
            if (!signal.aborted && activeTask) {
                await new Promise((r) => setTimeout(r, 500));
                pushState();
                completeTask(activeTask, "task_completed");
            }
        } catch (err: any) {
            if (!signal.aborted || err.message === "timeout") {
                const normalizedCause = normalizeFailureCause(err);
                log.error("Task execution failed natively", {
                    action: intent.action,
                    target: intent.target?.name,
                    raw_error: err.message,
                    normalized_cause: normalizedCause,
                });
                if (
                    normalizedCause === "STUCK" ||
                    normalizedCause === "PATH_FAILED"
                ) {
                    await executeAntiStuckReflex();
                }
                pushState();
                if (activeTask)
                    completeTask(activeTask, "task_failed", normalizedCause);
            }
        } finally {
            if (timeoutId) clearTimeout(timeoutId);
            if (currentTask?.id === activeTask?.id) currentTask = null;
            if (taskAbortController === localController)
                taskAbortController = null;
        }
    } catch (err: any) {
        log.error("Uncaught intent execution error", { err });
        client.sendPanic(err instanceof Error ? err : new Error(String(err)));
        throw err;
    }
}

function pushState() {
    if (!client) return;
    if (!bot?.entity || !isValidPosition(bot.entity?.position)) {
        const now = Date.now();
        if (now - lastStatePushTime < 3000) return;
        lastStatePushTime = now;
        client.sendState({
            health: 0,
            food: 0,
            time_of_day: 0,
            experience: 0,
            level: 0,
            position: { x: 0, y: 0, z: 0 },
            threats: [],
            pois: [],
            inventory: [],
            hotbar: Array(9).fill(null),
            offhand: null,
            active_slot: 0,
            known_chests: {},
            current_task: null,
            task_progress: 0.0,
        });
        return;
    }
    const hotbarStart = bot.inventory.hotbarStart || 36;
    const hotbar = Array.from({ length: 9 }, (_, i) => {
        const item = bot.inventory.slots[hotbarStart + i];
        return item ? { name: item.name, count: item.count } : null;
    });
    const offhandItem = bot.inventory.slots[45];
    const sig = `${bot.health}|${bot.food}|${Math.round(bot.entity.position.x)},${Math.round(bot.entity.position.y)},${Math.round(bot.entity.position.z)}|${bot.quickBarSlot}|${bot.inventory
        .items()
        .map((i) => `${i.name}:${i.count}`)
        .sort()
        .join(",")}|${Object.keys(knownChests).length}`;
    const now = Date.now();
    if (sig === lastStateSig && now - lastStatePushTime < 3000) return;
    lastStateSig = sig;
    lastStatePushTime = now;
    client.sendState({
        health: Math.round(bot.health),
        food: Math.round(bot.food),
        time_of_day: bot.time.timeOfDay,
        experience: bot.experience?.progress ?? 0,
        level: bot.experience?.level ?? 0,
        position: {
            x: bot.entity.position.x,
            y: bot.entity.position.y,
            z: bot.entity.position.z,
        },
        threats: getThreats(bot)
            .slice(0, 3)
            .map((t) => ({ name: t.name })),
        pois: getPOIs(bot),
        inventory: bot.inventory
            .items()
            .map((i) => ({ name: i.name, count: i.count })),
        hotbar: hotbar,
        offhand: offhandItem
            ? { name: offhandItem.name, count: offhandItem.count }
            : null,
        active_slot: bot.quickBarSlot,
        known_chests: knownChests,
        current_task: currentTask
            ? {
                  id: currentTask.id,
                  action: currentTask.action,
                  target: currentTask.target,
                  count: currentTask.count,
                  priority: 0,
                  rationale: "",
                  source: "client",
                  trace: currentTask.trace,
              }
            : null,
        task_progress: currentTask
            ? isPathfinding
                ? pathProgress
                : bot.pathfinder?.isMoving()
                  ? 0.5
                  : 0.05
            : 0.0,
    });
}

async function connectWithRetry(maxAttempts = 10) {
    if (reconnectAttempt > maxAttempts) {
        log.error("Max reconnection attempts reached. Shutting down.");
        isShuttingDown = true;
        process.exit(1);
    }
    if (bot) {
        try {
            if ((bot as any).viewer) {
                (bot as any).viewer.close();
                viewerStarted = false;
            }
            bot.removeAllListeners();
            bot.quit();
        } catch (e) {}
    }
    bot = mineflayer.createBot({
        host: "127.0.0.1",
        port: 25565,
        username: "CraftBot",
        version: "1.19",
    });
    bot.loadPlugin(pathfinder);
    bot.loadPlugin(collectBlock);
    if (!client) {
        client = new SynapticClient({
            url: runtimeConfig.bot_ws_url || runtimeConfig.ws_url,
        });
        client.on("dispatch", (i) => void executeIntent(i));
        client.on("abort", () => {
            if (survival?.isPanickingNow()) {
                log.debug("Ignoring planner abort during survival reflex");
                return;
            }
            abortActiveTask("unlock");
        });
        client.connect();
    }
    if (!survival) {
        survival = new SurvivalSystem(bot, client, {
            onInterrupt: (r) => {
                if (r === "panic_flee") abortActiveTask("survival_panic");
            },
            stopMovement: () => stopMovement(),
        });
    } else {
        survival.bot = bot;
    }
    bot.on("error", (err: Error) => {
        if (isIgnorableError(err)) return;
        log.warn("Mineflayer bot emitted error", { err: err.message });
    });
    bot.on("end", (reason) => {
        isBotSpawned = false;
        abortActiveTask("bot_disconnected");
        if (survival) survival.stop();
        if (isShuttingDown) {
            log.info("Bot disconnected cleanly for shutdown.");
            return;
        }
        const backoffMs = Math.min(1000 * Math.pow(2, reconnectAttempt), 30000);
        setTimeout(
            () => {
                reconnectAttempt++;
                connectWithRetry(maxAttempts);
            },
            backoffMs + Math.random() * 1000,
        );
    });
    bot.on("spawn", () => {
        reconnectAttempt = 1;
        isBotSpawned = true;

        // Emit respawn event if this is a respawn after death
        if (hasDied) {
            hasDied = false;
            log.info("Bot respawned after death");
            client.sendEvent("bot_respawn", {
                message: "Bot respawned after death",
                timestamp: Date.now(),
            });
        }

        log.info("Bot spawned successfully.");
        const movements = new Movements(bot);
        movements.canDig = true;
        movements.allow1by1towers = true;
        movements.allowParkour = true;
        movements.allowSprinting = true;
        movements.liquids = new Set(
            [
                bot.registry.blocksByName.water?.id,
                bot.registry.blocksByName.lava?.id,
            ].filter((id) => id !== undefined) as number[],
        );
        movements.digCost = 1.5;
        movements.scafoldingBlocks = [
            "dirt",
            "cobblestone",
            "netherrack",
            "stone",
        ]
            .map((name) => bot.registry.blocksByName[name]?.id)
            .filter((id) => id !== undefined);
        bot.pathfinder.setMovements(movements);

        // Increased pathfinder think timeout to 30s for complex terrain
        bot.pathfinder.thinkTimeout = 30000;

        bot.inventory.removeAllListeners("updateSlot");
        bot.inventory.on("updateSlot", pushState);
        bot.on("path_update", (r: any) => {
            isPathfinding = true;
            const nodesLeft = r.path?.length || 0;
            if (nodesLeft > 0) {
                pathProgress = Math.max(0.05, 1.0 - nodesLeft / 100);
            } else {
                pathProgress = 0.05;
            }
        });
        bot.on("goal_reached", () => {
            isPathfinding = false;
            pathProgress = 1.0;
        });
        pushState();
        client.connect();
        survival.start();
        if (runtimeConfig.enable_viewer && !viewerStarted) {
            try {
                viewer(bot, {
                    port: runtimeConfig.viewer_port,
                    firstPerson: true,
                    viewDistance: 4,
                });
                viewerStarted = true;
            } catch (err) {}
        }
        bot.removeAllListeners("physicsTick");
        bot.on("physicsTick", () => {
            if (!bot || !bot.entity || bot.health <= 0) return;
            if (bot.entity.velocity) {
                lastVelocity = {
                    x: bot.entity.velocity.x,
                    y: bot.entity.velocity.y,
                    z: bot.entity.velocity.z,
                };
            }
            if (!currentTask && !bot.pathfinder?.isMoving()) {
                if (Math.random() < 0.05) {
                    const entity = bot.nearestEntity(
                        (e) =>
                            (e.type === "mob" || e.type === "player") &&
                            bot.entity.position.distanceTo(e.position) < 16,
                    );
                    if (entity) {
                        bot.lookAt(
                            entity.position.offset(0, entity.height * 0.8, 0),
                            true,
                        ).catch(() => {});
                    }
                }
            }
        });
    });
    bot.on("message", (msg) => {
        const text = msg.toString();
        if (text.includes(bot.username) && !text.startsWith("<"))
            lastDeathMessage = text;
    });
    bot.on("death", () => {
        isBotSpawned = false;
        hasDied = true; // Set flag when bot dies
        abortActiveTask("bot_died");
        if (survival) survival.reset();
        client.sendEvent("death", {
            error: "bot_died",
            stack: lastDeathMessage,
        });
        lastDeathMessage = "unknown causes";
        setTimeout(() => {
            if (bot && !isShuttingDown) {
                log.info("Attempting to respawn...");
                bot.respawn();
            }
        }, 3000);
    });
    bot.on("windowOpen", (window) => {
        if (
            String(window.type).includes("chest") ||
            String(window.type).includes("generic")
        ) {
            const chestBlock = bot.findBlock({
                matching: bot.registry.blocksByName.chest?.id ?? -1,
                maxDistance: 6,
            });
            if (chestBlock) {
                const posKey = `${chestBlock.position.x},${chestBlock.position.y},${chestBlock.position.z}`;
                knownChests[posKey] = window
                    .containerItems()
                    .map((i) => ({ name: i.name, count: i.count }));
                pushState();
            }
        }
    });
    bot.on("health", pushState);
    bot.on("experience", pushState);
    // @ts-ignore
    bot.on("heldItemChanged", pushState);
    if (stateInterval) clearInterval(stateInterval);
    stateInterval = setInterval(pushState, 3000);
}

async function bootstrap() {
    runtimeConfig = await config.loadConfig();
    log.info("Bot configuration loaded", { ws_url: runtimeConfig.ws_url });
    await connectWithRetry();
}

process.on("SIGINT", () => {
    isShuttingDown = true;
    abortActiveTask("shutdown");
    if (stateInterval) clearInterval(stateInterval);
    if (bot) bot.quit();
    client?.disconnect();
    setTimeout(() => process.exit(0), 1000);
});

bootstrap().catch((err) => {
    log.error("Fatal startup", {
        err: err instanceof Error ? err.message : String(err),
        stack: err?.stack,
    });
    process.exit(1);
});
