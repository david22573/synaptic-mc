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

// ==========================================
// GLOBALS & STATE
// ==========================================

let currentTask: models.ActiveTask | null = null;
let taskAbortController: AbortController | null = null;
let bot: mineflayer.Bot;
let client: ControlPlaneClient;
let survival: SurvivalSystem;
let runtimeConfig: config.Config;

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
    task: models.ActiveTask | null,
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
    cause: string = "",
) {
    if (!task || !client) return;

    client.sendEvent(status, taskLabel(task), task.id, cause, task.startTime);

    if (currentTask?.id === task.id) {
        currentTask = null;
    }
}

function abortActiveTask(reason: string) {
    if (currentTask) {
        log.info("Aborting active task", {
            action: currentTask.action,
            reason,
        });
        if (taskAbortController) {
            taskAbortController.abort(reason);
            taskAbortController = null;
        }
        completeTask(currentTask, "task_aborted", reason);
    }
}

// ==========================================
// ORCHESTRATION
// ==========================================

async function executeDecision(decision: models.IncomingDecision) {
    if (!decision?.action) {
        log.error("Received malformed command", { decision });
        return;
    }

    if (survival && survival.isPanickingNow()) {
        log.warn("Rejecting command due to active panic lock", {
            action: decision.action,
        });
        client.sendEvent(
            "task_aborted",
            `${decision.action} ${decision.target?.name || "none"}`,
            decision.id,
            "panic_lock",
            Date.now(),
        );
        return;
    }

    log.info("Executing decision", {
        id: decision.id,
        action: decision.action,
        target_type: decision.target?.type,
        target_name: decision.target?.name,
        trace: decision.trace || {
            trace_id: "unknown",
            action_id: decision.id,
        },
    });

    abortActiveTask("preempted_by_new_command");

    taskAbortController = new AbortController();
    const signal = taskAbortController.signal;

    currentTask = {
        id: decision.id,
        action: decision.action,
        target: decision.target || { type: "none", name: "none" },
        startTime: Date.now(),
        trace: decision.trace || {
            trace_id: "unknown",
            action_id: decision.id,
        },
    };

    stopMovement();

    const activeTask = currentTask;

    client.sendEvent(
        "task_started",
        taskLabel(activeTask),
        activeTask.id,
        "",
        activeTask.startTime,
    );

    const timeoutMs = runtimeConfig.task_timeouts[decision.action] || 10000;
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
                runtimeConfig.task_timeouts,
                () => getThreats(bot),
                (threats) => computeSafeRetreat(bot, threats, 20),
                stopMovement,
            ),
            timeoutPromise,
        ]);

        if (!signal.aborted) {
            completeTask(activeTask, "task_completed");
        }
    } catch (err) {
        const message =
            err instanceof Error ? err.message : String(err || "unknown_error");

        if (signal.aborted || message === "aborted") {
            // Task already completed through abortActiveTask if aborted locally
            if (currentTask?.id === activeTask.id) {
                completeTask(activeTask, "task_aborted", message);
            }
        } else {
            log.error("Task failed", {
                id: decision.id,
                action: decision.action,
                error: message,
            });
            completeTask(activeTask, "task_failed", message);
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

    runtimeConfig = await config.loadConfig();

    log.info(
        `Config loaded. Connecting to Minecraft server at 127.0.0.1:25565...`,
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

    client = new ControlPlaneClient(runtimeConfig.ws_url, {
        onCommand: (decision) => void executeDecision(decision),
        onUnlock: () => {
            log.debug("Bot unlocked from control plane");
            abortActiveTask("control_plane_unlock");
        },
    });

    survival = new SurvivalSystem(bot, client, {
        onInterrupt: (reason: string) => {
            if (reason === "panic_flee") {
                abortActiveTask("survival_panic_reflex");
            }
        },
        stopMovement: () => stopMovement(),
    });

    bot.on("login", () => log.info("Bot logged in"));

    let isFirstSpawn = true;

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

            if (runtimeConfig.enable_viewer) {
                try {
                    viewer(bot, {
                        port: runtimeConfig.viewer_port,
                        firstPerson: true,
                        viewDistance: 2,
                    });

                    log.info("Prismarine viewer started", {
                        port: runtimeConfig.viewer_port,
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
        abortActiveTask("died");
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
