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

const bot = mineflayer.createBot({
    host: "localhost",
    port: 25565,
    username: "CraftBot",
    version: "1.19",
});

bot.loadPlugin(pathfinder);

const client = new ControlPlaneClient(config.WS_URL, {
    onCommand: (decision) => void executeDecision(decision),
    onUnlock: () => {
        log.debug("Bot unlocked from control plane");
    },
});

const survival = new SurvivalSystem(bot, client, {
    onInterrupt: (reason: string) => {
        if (taskAbortController) {
            log.info(`LLM Task interrupted by survival reflex: ${reason}`);
            taskAbortController.abort();
            taskAbortController = null;
        }
    },
    stopMovement: () => stopMovement(),
});

// ==========================================
// STATE HELPERS
// ==========================================

function taskLabel(task: Pick<models.ActiveTask, "action" | "target">) {
    return `${task.action} ${task.target?.name || "none"}`.trim();
}

function stopMovement() {
    try {
        bot.clearControlStates();
    } catch {}
    try {
        (bot as any).pathfinder.setGoal(null);
    } catch {}
}

function completeTask(
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
) {
    if (!currentTask) return;

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
// BOT LIFECYCLE
// ==========================================

bot.on("login", () => log.info("Bot logged in"));

bot.once("spawn", () => {
    log.info("Bot spawned", { env_path: config.ENV_PATH, cwd: process.cwd() });

    (bot as any).pathfinder.setMovements(new Movements(bot));
    client.connect();
    survival.start();

    if (config.ENABLE_VIEWER) {
        try {
            viewer(bot, {
                port: config.VIEWER_PORT,
                firstPerson: false,
                viewDistance: 2,
            });
            log.info("Prismarine viewer started", { port: config.VIEWER_PORT });
        } catch (err) {
            log.error("Viewer failed to start", {
                err: err instanceof Error ? err.message : String(err),
            });
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

    client.sendEvent(
        "death",
        "died",
        "",
        "killed_in_action", // Alternatively extract cause from chat logs if desired
        0,
    );
});

bot.on("kicked", (reason: unknown) => log.error("Bot was kicked", { reason }));
bot.on("end", (reason: unknown) =>
    log.error("Bot connection ended", { reason }),
);
bot.on("error", (err: unknown) =>
    log.error("Bot error", {
        err: err instanceof Error ? err.message : String(err),
    }),
);

// ==========================================
// TELEMETRY SYNC
// ==========================================

setInterval(() => {
    if (!bot.entity || !bot.entities) return;

    const state = {
        health: Math.round(bot.health),
        food: Math.round(bot.food),
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
