import pkg from "mineflayer-pathfinder";
import mineflayer from "mineflayer";
import { mineflayer as viewer } from "prismarine-viewer";

import * as config from "./lib/config.js";
import { log } from "./lib/logger.js";
import * as models from "./lib/models.js";
import { runTask } from "./lib/tasks/task.js";
import { ControlPlaneClient } from "./lib/network/client.js";
import { SurvivalSystem } from "./lib/systems/survival.js";
import { getThreats, computeSafeRetreat } from "./lib/utils/threats.js";

const { pathfinder, Movements } = pkg;

(log as any).warn = (msg: string, meta: Record<string, unknown> = {}) =>
    console.warn(
        JSON.stringify({
            level: "WARN",
            msg,
            ...meta,
            timestamp: new Date().toISOString(),
        }),
    );

// ==========================================
// GLOBALS & STATE
// ==========================================

let currentTask: models.ActiveTask | null = null;
let taskAbortController: AbortController | null = null;

let bot: mineflayer.Bot;
let client: ControlPlaneClient;
let survival: SurvivalSystem;

// ==========================================
// STATE HELPERS
// ==========================================

function taskLabel(task: Pick<models.ActiveTask, "action" | "target">) {
    return `${task.action} ${task.target?.name || "none"}`.trim();
}

function stopMovement() {
    if (!bot) return;
    try {
        bot.clearControlStates();
    } catch (err) {
        log.debug("Failed to clear control states", { err: String(err) });
    }
    try {
        if ((bot as any).pathfinder) {
            (bot as any).pathfinder.setGoal(null);
        }
    } catch (err) {
        log.debug("Failed to clear pathfinder goal", { err: String(err) });
    }
}

function completeTask(
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
) {
    if (!currentTask || !client) return;

    client.sendEvent(
        status,
        taskLabel(currentTask),
        currentTask.id,
        "",
        currentTask.startTime,
    );

    currentTask = null;
}

// ==========================================
// ORCHESTRATION
// ==========================================

async function executeDecision(decision: models.IncomingDecision) {
    if (!decision?.action) {
        log.error("Received malformed command", { decision });
        return;
    }

    log.info("Executing decision", {
        id: decision.id,
        action: decision.action,
        target_type: decision.target?.type,
        target_name: decision.target?.name,
    });

    if (taskAbortController) {
        taskAbortController.abort();
    }

    taskAbortController = new AbortController();
    const signal = taskAbortController.signal;

    currentTask = {
        id: decision.id,
        action: decision.action,
        target: decision.target || { type: "none", name: "none" },
        startTime: Date.now(),
    };

    stopMovement();

    client.sendEvent(
        "task_started",
        taskLabel(currentTask),
        decision.id,
        "",
        currentTask.startTime,
    );

    const timeoutMs = config.TASK_TIMEOUTS[decision.action] || 10000;
    let timeoutId: NodeJS.Timeout | null = null;

    const timeoutPromise = new Promise<never>((_, reject) => {
        timeoutId = setTimeout(() => reject(new Error("timeout")), timeoutMs);
    });

    try {
        await Promise.race([
            runTask(
                bot,
                decision,
                signal,
                config.TASK_TIMEOUTS,
                () => getThreats(bot),
                (threats) => computeSafeRetreat(bot, threats),
                stopMovement,
            ),
            timeoutPromise,
        ]);

        if (!signal.aborted) {
            completeTask("task_completed");
        }
    } catch (err) {
        const message =
            err instanceof Error ? err.message : String(err || "unknown_error");

        if (signal.aborted || message === "aborted") {
            completeTask("task_aborted");
        } else {
            log.error("Task failed", {
                id: decision.id,
                action: decision.action,
                error: message,
            });
            completeTask("task_failed");
        }
    } finally {
        if (timeoutId) clearTimeout(timeoutId);
        stopMovement();
        if (taskAbortController?.signal === signal) {
            taskAbortController = null;
        }
    }
}

// ==========================================
// BOT LIFECYCLE & BOOTSTRAP
// ==========================================

async function bootstrap() {
    log.info("Bootstrapping bot... fetching dynamic config.");
    await config.loadConfig();

    log.info(
        "Config loaded. Connecting to Minecraft server at 127.0.0.1:25565...",
    );

    bot = mineflayer.createBot({
        host: "127.0.0.1",
        port: 25565,
        username: "CraftBot",
        version: "1.19",
    });

    bot.on("error", (err: unknown) =>
        log.error("Bot error", {
            err: err instanceof Error ? err.message : String(err),
        }),
    );
    bot.on("end", (reason: unknown) =>
        log.error("Bot connection ended", { reason }),
    );
    bot.on("kicked", (reason: unknown) =>
        log.error("Bot was kicked", { reason }),
    );

    bot.loadPlugin(pathfinder);

    client = new ControlPlaneClient(config.WS_URL, {
        onCommand: (decision) => void executeDecision(decision),
        onUnlock: () => {
            log.debug("Bot unlocked from control plane");
        },
    });

    survival = new SurvivalSystem(bot, client, {
        onInterrupt: (reason: string) => {
            if (taskAbortController) {
                log.info(`LLM Task interrupted by survival reflex: ${reason}`);
                taskAbortController.abort();
                taskAbortController = null;
            }
        },
        stopMovement: () => stopMovement(),
    });

    bot.on("login", () => log.info("Bot logged in"));

    let isFirstSpawn = true;

    // Use .on instead of .once so movements are re-injected when respawning
    bot.on("spawn", () => {
        log.info("Bot spawned", {
            env_path: config.ENV_PATH,
            cwd: process.cwd(),
            isFirstSpawn,
        });

        const movements = new Movements(bot);
        movements.canDig = true;

        const leafNames = [
            "oak_leaves",
            "birch_leaves",
            "spruce_leaves",
            "jungle_leaves",
            "acacia_leaves",
            "dark_oak_leaves",
            "mangrove_leaves",
            "azalea_leaves",
            "flowering_azalea_leaves",
            "cherry_leaves",
        ];
        for (const name of leafNames) {
            const block = bot.registry.blocksByName[name];
            if (block) {
                movements.blocksToAvoid.delete(block.id);
            }
        }
        (bot as any).pathfinder.setMovements(movements);

        if (isFirstSpawn) {
            isFirstSpawn = false;
            client.connect();
            survival.start();

            if (config.ENABLE_VIEWER) {
                try {
                    viewer(bot, {
                        port: config.VIEWER_PORT,
                        firstPerson: true,
                        viewDistance: 2,
                    });
                    log.info("Prismarine viewer started", {
                        port: config.VIEWER_PORT,
                    });
                } catch (err) {
                    log.error("Viewer failed to start", {
                        err: err instanceof Error ? err.message : String(err),
                    });
                }
            }
        }
    });

    bot.on("death", () => {
        log.warn("Bot died; resetting local execution state");
        if (taskAbortController) {
            taskAbortController.abort();
            taskAbortController = null;
        }
        currentTask = null;
        stopMovement();

        survival.reset();
        client.sendEvent("death", "died", "", "killed_in_action", 0);
    });

    // ==========================================
    // TELEMETRY SYNC
    // ==========================================
    setInterval(() => {
        if (!bot || !bot.entity || !bot.entities) return;

        const nearbyBed = bot.findBlock({
            maxDistance: 32,
            matching: (block: any) => block?.name.includes("bed"),
        });

        const state = {
            health: Math.round(bot.health),
            food: Math.round(bot.food),
            time_of_day: bot.time.timeOfDay,
            has_bed_nearby: !!nearbyBed,
            position: {
                x: Math.round(bot.entity.position.x),
                y: Math.round(bot.entity.position.y),
                z: Math.round(bot.entity.position.z),
            },
            threats: getThreats(bot)
                .slice(0, 3)
                .map((t) => ({ name: t.name })),
            inventory: bot.inventory
                .items()
                .map((i) => ({ name: i.name, count: i.count })),
        };

        client.sendState(state);
    }, 2000);
}

// Start the engine
bootstrap().catch((err) => {
    log.error("Fatal startup error", { err: String(err) });
    process.exit(1);
});
