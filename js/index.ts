import pkg from "mineflayer-pathfinder";
import mineflayer from "mineflayer";
import { mineflayer as viewer } from "prismarine-viewer";
import { plugin as collectBlock } from "mineflayer-collectblock";
import * as config from "./lib/config.js";
import { log } from "./lib/logger.js";
import * as models from "./lib/models.js";
import { runTask, calculateDynamicTimeout } from "./lib/tasks/task.js";
import { SynapticClient } from "./lib/network/client.js";
import { SurvivalSystem } from "./lib/systems/survival.js";
import { BotController } from "./lib/control/controller.js";
import { getThreats, computeSafeRetreat } from "./lib/utils/threats.js";
import { getPOIs, clearPOICache } from "./lib/utils/perception.js";
import { senseWorld } from "./lib/utils/world.js";

const { pathfinder, Movements, goals } = pkg;

let viewerStarted = false;

interface NetworkError extends Error {
    code?: string;
}

function isValidPosition(pos: models.Vec3 | undefined): boolean {
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

const isIgnorableError = (err: NetworkError | unknown): boolean => {
    if (!err) return false;
    const error = err as NetworkError;
    const msg = String(error.message || error.stack || error);
    const name = String(error.name || "");
    const code = String(error.code || "");

    const protocolErrors =
        name.includes("PartialReadError") ||
        msg.includes("VarInt") ||
        msg.includes("protodef");

    if (protocolErrors) {
        log.warn(
            "Protocol parsing error detected. World state may be inconsistent.",
            { error: msg },
        );
        recordProtocolError(msg);
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
console.error = (...args: unknown[]) => {
    const firstArg = args[0];
    const error = firstArg as any;
    const msg =
        typeof firstArg === "object"
            ? String(error?.err?.message || error?.message || "")
            : String(firstArg);

    if (
        isIgnorableError(firstArg) ||
        msg.includes("PartialReadError") ||
        msg.includes("protodef")
    )
        return;

    originalConsoleError.apply(console, args);
};

process.on("uncaughtException", (err: Error) => {
    if (isIgnorableError(err)) return;
    log.error("Fatal uncaught exception", {
        err: err.message || String(err),
        stack: err.stack || "No stack trace available",
    });
    process.exit(1);
});

process.on("unhandledRejection", (reason: unknown) => {
    if (isIgnorableError(reason)) return;
    log.error("Unhandled promise rejection", { reason });
});

let currentTask: models.ActiveTask | null = null;
let preloadedTask: models.ActionIntent | null = null;
let taskAbortController: AbortController | null = null;

let bot: mineflayer.Bot;
let client: SynapticClient;
let survival: SurvivalSystem;
let controller: BotController;
let runtimeConfig: config.Config;

let lastDeathMessage: string = "unknown causes";
let lastStateSig = "";
let lastStatePushTime = 0;

let reconnectAttempt = 1;
let stateInterval: NodeJS.Timeout | null = null;
let isShuttingDown = false;
let isBotSpawned = false;
let protocolErrorTimestamps: number[] = [];
let protocolRecoveryInProgress = false;
let manualReconnectPending = false;
let lastProtocolErrorAt = 0;
let lastProtocolLogAt = 0;

let hasDied = false;

let isPathfinding = false;
let pathProgress = 0.0;

function stopMovement() {
    if (!bot || !bot.entity) return;
    try {
        bot.clearControlStates();
        if (bot.pathfinder) {
            bot.pathfinder.setGoal(null);
        }
    } catch (e) {
        log.debug("Failed to stop movement cleanly", { error: e });
    }
}

function scheduleProtocolRecovery(reason: string) {
    if (!bot || isShuttingDown || protocolRecoveryInProgress) return;

    protocolRecoveryInProgress = true;
    manualReconnectPending = true;
    protocolErrorTimestamps = [];
    isBotSpawned = false;
    clearPOICache();
    survival?.reset();
    controller?.stop();
    abortActiveTask("protocol_desync");

    log.warn("Protocol stream desynced; forcing reconnect", { reason });

    try {
        bot.removeAllListeners();
        bot.quit(reason);
    } catch {
        try {
            bot.end(reason);
        } catch {}
    }

    setTimeout(() => {
        if (!isShuttingDown) {
            reconnectAttempt = 1;
            void connectWithRetry();
        }
    }, 1500);
}

function recordProtocolError(reason: string) {
    const now = Date.now();
    lastProtocolErrorAt = now;
    protocolErrorTimestamps.push(now);
    protocolErrorTimestamps = protocolErrorTimestamps.filter(
        (ts) => now - ts < 5000,
    );

    const isSpawnBurst = !isBotSpawned && protocolErrorTimestamps.length >= 3;
    const isSustainedBurst = protocolErrorTimestamps.length >= 5;

    if (isSpawnBurst || isSustainedBurst) {
        scheduleProtocolRecovery(reason);
    }
}

function handleBotProtocolError(err: Error | unknown) {
    if (!err) return;
    const error = err as Error;

    const msg = String(error.message || error.stack || error);
    const name = String(error.name || "");
    const isProtocolError =
        name.includes("PartialReadError") ||
        msg.includes("VarInt") ||
        msg.includes("protodef") ||
        msg.includes("entity_metadata");

    if (!isProtocolError) {
        log.error("Bot runtime error", {
            err: error.message || String(error),
            stack: error.stack,
        });
        return;
    }

    recordProtocolError(msg);

    if (Date.now() - lastProtocolLogAt < 250) return;
    lastProtocolLogAt = Date.now();

    log.warn("Malformed Minecraft protocol packet received", {
        error: msg,
        version: bot?.version,
        protocolVersion: bot?.protocolVersion,
    });
}

function completeTask(
    task: models.ActiveTask | null,
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
    cause: string = "",
    progress: number = 0,
) {
    if (!task || !client) return;
    if (currentTask?.id === task.id) currentTask = null;

    client.sendEvent(
        status,
        task.action,
        task.target?.name || "none",
        task.id,
        cause,
        task.startTime,
        progress,
    );

    // If we have a preloaded task and the previous one just finished, execute it immediately
    if (preloadedTask && !currentTask && !isShuttingDown) {
        const next = preloadedTask;
        preloadedTask = null;
        log.info("Immediate transition to preloaded task", { action: next.action });
        void executeIntent(next);
    }
}

function abortActiveTask(reason: string) {
    if (currentTask && taskAbortController) {
        taskAbortController.abort(reason);
        stopMovement();
        completeTask(currentTask, "task_aborted", reason, 0);
        currentTask = null;
        taskAbortController = null;
    }
}

function normalizeFailureCause(err: unknown): string {
    const error = err as Error;
    const msg = String(error.message || error).toLowerCase();
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

async function executeAntiStuckReflex(signal?: AbortSignal) {
    if (!bot || !bot.entity || bot.health <= 0 || signal?.aborted) return;
    log.warn("STUCK DETECTED in navigator: Triggering quick jump recovery");

    try {
        bot.setControlState("jump", true);
        const directions: mineflayer.ControlState[] = ["left", "right", "back"];
        const dir = directions[Math.floor(Math.random() * directions.length)];
        bot.setControlState(dir, true);

        await new Promise((r) => setTimeout(r, 500));
        if (signal?.aborted) return;
        bot.clearControlStates();

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
                if (signal?.aborted) return;
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
            log.debug("Dropping intent: bot not alive/spawned", { action: intent.action });
            return;
        }

        if (survival?.isPanickingNow() && intent.action !== "retreat") {
            client.sendEvent(
                "task_aborted",
                "lock",
                intent.target?.name || "none",
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
            ...intent,
            startTime: Date.now(),
        };

        isPathfinding = false;
        pathProgress = 0.0;

        // EMIT IMMEDIATELY TO SYNC STATE
        client.sendEvent(
            "task_start",
            `${intent.action}`,
            intent.target?.name || "none",
            intent.id,
            "",
            Date.now(),
        );

        const activeTask = currentTask;
        let timeoutId: NodeJS.Timeout | undefined;

        try {
            const timeouts =
                runtimeConfig.task_timeouts || config.TASK_TIMEOUTS;
            const timeoutMs = calculateDynamicTimeout(intent, bot, timeouts);

            const result = await Promise.race([
                runTask(
                    bot,
                    intent,
                    signal,
                    timeouts,
                    () => getThreats(bot),
                    (t) => computeSafeRetreat(bot, t, 20),
                    stopMovement,
                ),
                new Promise<models.ExecutionResult>((_, r) => {
                    timeoutId = setTimeout(() => {
                        localController.abort("timeout");
                        stopMovement();
                        r(new Error("timeout"));
                    }, timeoutMs);
                }),
            ]);

            if (activeTask && currentTask?.id === activeTask.id) {
                if (!signal.aborted) {
                    await new Promise((r) => setTimeout(r, 200));
                    pushState();

                    if (result && result.success === false) {
                        completeTask(
                            activeTask,
                            "task_failed",
                            result.cause,
                            result.progress,
                        );
                    } else {
                        completeTask(
                            activeTask,
                            "task_completed",
                            "",
                            result?.progress ?? 1.0,
                        );
                    }
                } else {
                    completeTask(
                        activeTask,
                        "task_aborted",
                        String(signal.reason) || "preempted",
                        result?.progress ?? 0,
                    );
                }
            }
        } catch (err: unknown) {
            const error = err as Error;
            if (!signal.aborted || error.message === "timeout") {
                const normalizedCause = normalizeFailureCause(err);
                log.warn(`Task failed: ${intent.action}`, {
                    cause: normalizedCause,
                    error: error.message,
                });

                if (
                    normalizedCause === "STUCK" ||
                    normalizedCause === "TIMEOUT"
                ) {
                    await executeAntiStuckReflex(signal);
                }

                pushState();

                if (activeTask) {
                    completeTask(activeTask, "task_failed", normalizedCause, 0);
                } else if (intent.id) {
                    client.sendEvent(
                        "task_failed",
                        intent.action,
                        intent.target?.name || "none",
                        intent.id,
                        normalizedCause,
                        Date.now(),
                    );
                }
            }
        } finally {
            if (timeoutId) clearTimeout(timeoutId);
        }
    } catch (err: unknown) {
        const error = err as Error;
        log.error("Uncaught intent execution error", { err: error });
        client.sendPanic(error instanceof Error ? error : new Error(String(err)));
    }
}

function pushState(force = false) {
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
    const px = hasValidPos ? Math.round(pos.x!) : 0;
    const py = hasValidPos ? Math.round(pos.y!) : 0;
    const pz = hasValidPos ? Math.round(pos.z!) : 0;

    const sig = `${bot.health}|${bot.food}|${px},${py},${pz}|${inventoryItems}`;

    // Meaningful change check for high-frequency pusher
    let meaningful = force || !lastStateSig;
    if (!meaningful) {
        const lastParts = lastStateSig.split("|");
        const lastHealth = Number(lastParts[0]);
        const lastFood = Number(lastParts[1]);
        const [lpx, lpy, lpz] = lastParts[2].split(",").map(Number);

        const dist = Math.sqrt(
            Math.pow(px - lpx, 2) +
                Math.pow(py - lpy, 2) +
                Math.pow(pz - lpz, 2)
        );

        // Meaningful if: stats changed, moved > 5 blocks, or threats present
        if (
            dist > 5 ||
            Math.round(bot.health) !== Math.round(lastHealth) ||
            Math.round(bot.food) !== Math.round(lastFood) ||
            getThreats(bot).length > 0
        ) {
            meaningful = true;
        }
    }

    if (!meaningful && now - lastStatePushTime < 3000) return;

    lastStateSig = sig;
    lastStatePushTime = now;

    const liveThreats = getThreats(bot);
    const world = senseWorld(bot, liveThreats);

    const state: models.GameState = {
        health: Math.round(bot.health || 0),
        food: Math.round(bot.food || 0),
        time_of_day: bot.time?.timeOfDay ?? 0,
        experience: bot.experience?.points ?? 0,
        experience_progress: bot.experience?.progress ?? 0,
        level: bot.experience?.level ?? 0,
        has_bed_nearby:
            bot.findBlock({
                matching: (b) => b.name.includes("bed"),
                maxDistance: 5,
            }) !== null,
        position: { x: px, y: py, z: pz },
        inventory: bot.inventory
            ? bot.inventory.items().map((i) => ({
                  name: i.name,
                  count: i.count,
              }))
            : [],
        hotbar: bot.inventory
            ? bot.inventory.slots.slice(36, 45).map((s) => {
                  if (!s) return null;
                  return { name: s.name, count: s.count };
              })
            : [],
        offhand:
            bot.inventory && bot.inventory.slots[45]
                ? {
                      name: bot.inventory.slots[45].name,
                      count: bot.inventory.slots[45].count,
                  }
                : null,
        active_slot: bot.quickBarSlot || 0,
        pois: hasValidPos ? getPOIs(bot) : [],
        threats: liveThreats.map((t) => ({
            name: t.name,
            distance: t.distance,
        })),
        feedback: world.feedback,
        current_task: currentTask,
        task_progress: isPathfinding
            ? pathProgress
            : bot.pathfinder?.isMoving()
              ? 0.5
              : 0,
        danger_zones: world.dangerZones,
        terrain_roughness: world.terrainRoughness,
    };

    client.sendState(state);
}

async function connectWithRetry(maxAttempts = 10) {
    if (reconnectAttempt > maxAttempts) {
        process.exit(1);
    }

    if (bot) {
        try {
            bot.removeAllListeners();
            bot.quit();
        } catch (e) {
            log.debug("Bot quit error", { error: e });
        }
    }

    const mcHost = process.env.MC_HOST || "david22573.aternos.me";
    const mcPort = process.env.MC_PORT ? parseInt(process.env.MC_PORT) : 25565;
    bot = mineflayer.createBot({
        host: mcHost,
        port: mcPort,
        username: "SynapticBot",
        version: "1.19",
        hideErrors: true,
        logErrors: false,
    });

    bot.loadPlugin(pathfinder);
    bot.loadPlugin(collectBlock);
    bot.on("error", handleBotProtocolError);

    controller = new BotController(bot, () => getThreats(bot));
    (bot as any).controller = controller;

    if (!client) {
        client = new SynapticClient({
            url: runtimeConfig.bot_ws_url || runtimeConfig.ws_url,
        });
        client.on("dispatch", (i: models.ActionIntent) => {
            log.info("Received task dispatch", {
                action: i.action,
                target: i.target?.name,
                id: i.id,
            });
            void executeIntent(i);
        });
        client.on("preload", (i: models.ActionIntent) => {
            log.info("Received task preload", {
                action: i.action,
                target: i.target?.name,
                id: i.id,
            });
            preloadedTask = i;
        });
        client.on("abort", (payload?: { reason?: string }) => {
            const reason = payload?.reason || "unlock";

            if (
                currentTask?.action === "retreat" &&
                (survival?.isPanickingNow() || reason === "plan_invalidated")
            ) {
                return;
            }

            if (!survival?.isPanickingNow()) abortActiveTask(reason);
        });
        client.connect();
    }

    if (!survival) {
        survival = new SurvivalSystem(bot, {
            onInterrupt: (r: string) => {
                if (r.startsWith("panic_")) {
                    abortActiveTask("survival_panic");
                    pushState();
                }
            },
            stopMovement: () => stopMovement(),
            onPanicStart: (cause: string) => {
                client.sendEvent(
                    "panic_retreat_start",
                    "evasion",
                    "none",
                    "",
                    cause,
                    0,
                );
                pushState();
            },
            onPanicEnd: (cause: string) => {
                if (currentTask?.action === "retreat") {
                    abortActiveTask("unlock");
                }
                client.sendEvent(
                    "panic_retreat_end",
                    "evasion_complete",
                    "none",
                    "",
                    cause,
                    0,
                );
                pushState();
            },
        });
    } else {
        survival.bot = bot;
        survival.reset();
    }

    bot.on("spawn", () => {
        reconnectAttempt = 1;
        isBotSpawned = true;
        protocolRecoveryInProgress = false;
        manualReconnectPending = false;
        protocolErrorTimestamps = [];
        lastProtocolErrorAt = 0;
        lastProtocolLogAt = 0;
        log.info("Bot spawned successfully.", {
            version: bot.version,
            protocolVersion: bot.protocolVersion,
        });
        clearPOICache();

        const movements = new Movements(bot);
        movements.canDig = true;
        movements.allowSprinting = true;
        movements.allowParkour = true;
        movements.allow1by1towers = true;
        movements.allowFreeMotion = true;

        bot.pathfinder.setMovements(movements);
        bot.pathfinder.thinkTimeout = 20000;

        // Auto-satiate for testing to ensure sprinting isn't blocked by hunger
        bot.chat("/effect give @s saturation infinite 255 true");

        survival.start();
        controller.start();
        lastStateSig = "";
        lastStatePushTime = 0;
        pushState();

        if (hasDied) {
            client.sendEvent("bot_respawn", { status: "respawned" });
            hasDied = false;
        }

        if (runtimeConfig.enable_viewer && !viewerStarted) {
            viewer(bot, { port: runtimeConfig.viewer_port, firstPerson: true });
            viewerStarted = true;
        }
    });

    bot.on("death", () => {
        isBotSpawned = false;
        hasDied = true;
        survival.reset();
        clearPOICache();
        abortActiveTask("bot_died");
        client.sendEvent("death", { error: "bot_died" });
        setTimeout(() => {
            if (bot && !isShuttingDown) bot.respawn();
        }, 3000);
    });

    bot.on("end", () => {
        isBotSpawned = false;
        clearPOICache();
        if (isShuttingDown) return;
        if (manualReconnectPending) return;
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
    
    // Heartbeat push
    stateInterval = setInterval(() => pushState(true), 3000);
    
    // High-frequency polling for change-driven pushes
    setInterval(() => {
        if (isBotSpawned && bot.entity) {
            pushState(false);
        }
    }, 250);
}

process.on("SIGINT", () => {
    isShuttingDown = true;
    if (controller) controller.stop();
    if (bot) bot.quit();
    process.exit(0);
});

bootstrap().catch((err: Error) => {
    log.error("Fatal startup", { err: err.message });
    process.exit(1);
});
