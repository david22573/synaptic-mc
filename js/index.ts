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
import { getPOIs, clearPOICache } from "./lib/utils/perception.js";

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

/**
 * FIXED: Refined ignorable errors.
 * While we suppress crashes, we now log warnings for PartialReadErrors
 * as they indicate the bot's world state is likely corrupted/out of sync.
 */
const isIgnorableError = (err: Error | any): boolean => {
    if (!err) return false;
    const msg = String(err?.message || err?.stack || err);
    const name = String(err?.name || "");
    const code = String(err?.code || "");

    const protocolErrors =
        name.includes("PartialReadError") ||
        msg.includes("VarInt") ||
        msg.includes("protodef");

    if (protocolErrors) {
        log.warn(
            "Protocol parsing error detected. World state may be inconsistent.",
            { error: msg },
        );
        return true;
    }

    return (
        msg.includes("Read error for undefined") ||
        msg.includes("Missing characters in string") ||
        (msg.includes("size is") && msg.includes("expected size")) ||
        msg.includes("Unexpected server response") ||
        code === "ECONNRESET" ||
        code === "ECONNREFUSED" ||
        code === "EADDRINUSE" ||
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

let hasDied = false;

const knownChests: Record<string, { name: string; count: number }[]> = {};
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
        msg.includes("timeout") ||
        msg.includes("failed_to_reach")
    )
        return "TIMEOUT";

    if (msg.includes("stuck")) return "STUCK";

    if (
        msg.includes("no_entity") ||
        msg.includes("missing_entity") ||
        msg.includes("target_lost")
    )
        return "NO_ENTITY";

    if (msg.includes("missing_ingredients") || msg.includes("no_blocks"))
        return "MISSING_RESOURCES";

    return "UNKNOWN";
}

/**
 * FIXED: Enhanced Anti-Stuck Reflex.
 * Adds more aggressive movement and block clearing to escape environmental traps.
 */
async function executeAntiStuckReflex() {
    if (!bot || !bot.entity || bot.health <= 0) return;
    log.warn("STUCK DETECTED in navigator: Triggering quick jump recovery");

    try {
        // Strategy 1: Jump and random strafe
        bot.setControlState("jump", true);
        const directions: mineflayer.ControlState[] = ["left", "right", "back"];
        const dir = directions[Math.floor(Math.random() * directions.length)];
        bot.setControlState(dir, true);

        await new Promise((r) => setTimeout(r, 500));
        bot.clearControlStates();

        // Strategy 2: Break local obstructions
        const pos = bot.entity.position.floored();
        const yaw = bot.entity.yaw;
        const dx = -Math.sin(yaw);
        const dz = -Math.cos(yaw);

        const headBlock = bot.blockAt(
            pos.offset(Math.round(dx), 1, Math.round(dz)),
        );
        const legBlock = bot.blockAt(
            pos.offset(Math.round(dx), 0, Math.round(dz)),
        );

        for (const block of [headBlock, legBlock]) {
            if (
                block &&
                block.boundingBox === "block" &&
                block.name !== "bedrock"
            ) {
                log.info(`Anti-stuck: Clearing obstacle ${block.name}`);
                const tool = bot.pathfinder.bestHarvestTool(block);
                if (tool) await bot.equip(tool.type, "hand");
                await bot.dig(block);
                break;
            }
        }
    } catch (e) {
        log.debug("Anti-stuck reflex failed", { err: e });
    }
}

async function executeIntent(intent: models.ActionIntent) {
    try {
        if (!intent?.action || isShuttingDown) return;

        if (!bot || !bot.entity || bot.health <= 0 || !isBotSpawned) {
            client.sendEvent(
                "task_failed",
                `${intent.action}`,
                intent.id,
                "INVALID_BOT_STATE",
                Date.now(),
            );
            return;
        }

        if (survival?.isPanickingNow() && intent.action !== "retreat") {
            client.sendEvent(
                "task_aborted",
                "lock",
                intent.id,
                "panic",
                Date.now(),
            );
            return;
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
            `${intent.action}`,
            intent.id,
            "",
            Date.now(),
        );

        const activeTask = currentTask;
        let timeoutId: NodeJS.Timeout | undefined;

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

            if (
                !signal.aborted &&
                activeTask &&
                currentTask?.id === activeTask.id
            ) {
                await new Promise((r) => setTimeout(r, 200));
                pushState();
                completeTask(activeTask, "task_completed");
            }
        } catch (err: any) {
            if (!signal.aborted || err.message === "timeout") {
                const normalizedCause = normalizeFailureCause(err);

                if (
                    normalizedCause === "STUCK" ||
                    normalizedCause === "TIMEOUT"
                ) {
                    await executeAntiStuckReflex();
                }

                pushState();
                if (activeTask && currentTask?.id === activeTask.id) {
                    completeTask(activeTask, "task_failed", normalizedCause);
                }
            }
        } finally {
            if (timeoutId) clearTimeout(timeoutId);
        }
    } catch (err: any) {
        log.error("Uncaught intent execution error", { err });
        client.sendPanic(err instanceof Error ? err : new Error(String(err)));
    }
}

function pushState() {
    if (!client || !bot || !isBotSpawned) return;

    const now = Date.now();
    const pos = bot.entity?.position;
    const hasValidPos = isValidPosition(pos);

    const inventoryItems = bot.inventory
        ? bot.inventory
              .items()
              .map((i) => `${i.name}:${i.count}`)
              .sort()
              .join(",")
        : "";
    const px = hasValidPos ? Math.round(pos.x) : 0;
    const py = hasValidPos ? Math.round(pos.y) : 0;
    const pz = hasValidPos ? Math.round(pos.z) : 0;

    const sig = `${bot.health}|${bot.food}|${px},${py},${pz}|${inventoryItems}`;
    if (sig === lastStateSig && now - lastStatePushTime < 2000) return;

    lastStateSig = sig;
    lastStatePushTime = now;

    client.sendState({
        health: Math.round(bot.health || 0),
        food: Math.round(bot.food || 0),
        time_of_day: bot.time?.timeOfDay ?? 0,
        position: { x: px, y: py, z: pz },
        inventory: bot.inventory
            ? bot.inventory
                  .items()
                  .map((i) => ({ name: i.name, count: i.count }))
            : [],
        current_task: currentTask
            ? { ...currentTask, priority: 0, rationale: "", source: "client" }
            : null,
        task_progress: isPathfinding
            ? pathProgress
            : bot.pathfinder?.isMoving()
              ? 0.5
              : 0,
        pois: hasValidPos ? getPOIs(bot) : [],
    });
}

async function connectWithRetry(maxAttempts = 10) {
    if (reconnectAttempt > maxAttempts) {
        process.exit(1);
    }

    if (bot) {
        try {
            bot.removeAllListeners();
            bot.quit();
        } catch (e) {}
    }

    // FIXED: Explicitly set version to 1.19.1 to stabilize protodef parsing
    bot = mineflayer.createBot({
        host: "127.0.0.1",
        port: 25565,
        username: "SynapticBot",
        version: "1.19.1",
    });

    bot.loadPlugin(pathfinder);
    bot.loadPlugin(collectBlock);

    if (!client) {
        client = new SynapticClient({
            url: runtimeConfig.bot_ws_url || runtimeConfig.ws_url,
        });
        client.on("dispatch", (i) => void executeIntent(i));
        client.on("abort", () => {
            if (!survival?.isPanickingNow()) abortActiveTask("unlock");
        });
        client.connect();
    }

    if (!survival) {
        survival = new SurvivalSystem(bot, {
            onInterrupt: (r: string) => {
                if (r === "panic_flee") abortActiveTask("survival_panic");
            },
            stopMovement: () => stopMovement(),
        });
    } else {
        survival.bot = bot;
    }

    bot.on("spawn", () => {
        reconnectAttempt = 1;
        isBotSpawned = true;
        log.info("Bot spawned successfully.");

        const movements = new Movements(bot);
        movements.canDig = true;
        movements.allowSprinting = true;
        bot.pathfinder.setMovements(movements);
        bot.pathfinder.thinkTimeout = 60000;

        survival.start();
        pushState();

        if (runtimeConfig.enable_viewer && !viewerStarted) {
            viewer(bot, { port: runtimeConfig.viewer_port, firstPerson: true });
            viewerStarted = true;
        }
    });

    bot.on("death", () => {
        isBotSpawned = false;
        abortActiveTask("bot_died");
        client.sendEvent("death", { error: "bot_died" });
        setTimeout(() => {
            if (bot && !isShuttingDown) bot.respawn();
        }, 3000);
    });

    bot.on("end", () => {
        isBotSpawned = false;
        if (isShuttingDown) return;
        const backoff = Math.min(1000 * Math.pow(2, reconnectAttempt), 30000);
        setTimeout(() => {
            reconnectAttempt++;
            connectWithRetry();
        }, backoff);
    });
}

async function bootstrap() {
    runtimeConfig = await config.loadConfig();
    await connectWithRetry();
    stateInterval = setInterval(pushState, 3000);
}

process.on("SIGINT", () => {
    isShuttingDown = true;
    if (bot) bot.quit();
    process.exit(0);
});

bootstrap().catch((err) => {
    log.error("Fatal startup", { err: err.message });
    process.exit(1);
});
