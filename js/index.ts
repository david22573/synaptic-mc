import pkg from "mineflayer-pathfinder";
import mineflayer from "mineflayer";
import { mineflayer as viewer } from "prismarine-viewer";

import * as config from "./lib/config.js";
import { log } from "./lib/logger.js";
import * as models from "./lib/models.js";
import { runTask } from "./lib/tasks/task.js";
import { ControlPlaneClient } from "./lib/network/client.js";

const { pathfinder, Movements, goals } = pkg;

// Patch missing warn helper without changing logger shape everywhere
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
// INITIALIZATION
// ==========================================

const bot = mineflayer.createBot({
    host: "localhost",
    port: 25565,
    username: "CraftBot",
    version: "1.19",
});

bot.loadPlugin(pathfinder);

let currentTask: models.ActiveTask | null = null;
let awaitingCommand = false;
let reflexActive = false;
let reflexTimeout: NodeJS.Timeout | null = null;
let lastReflexTime = 0;
let taskAbortController: AbortController | null = null;

const client = new ControlPlaneClient(config.WS_URL, {
    onCommand: (decision) => {
        awaitingCommand = false;
        void executeDecision(decision);
    },
    onUnlock: () => {
        awaitingCommand = false;
    },
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
    try {
        (bot as any).pathfinder.stop();
    } catch {}
}

function clearReflexState() {
    reflexActive = false;
    if (reflexTimeout) {
        clearTimeout(reflexTimeout);
        reflexTimeout = null;
    }
}

function resetExecutionState() {
    if (taskAbortController) {
        taskAbortController.abort();
        taskAbortController = null;
    }

    currentTask = null;
    awaitingCommand = false;
    clearReflexState();
    stopMovement();
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
// THREATS / REFLEXES (Next refactor target)
// ==========================================

function getThreats(): models.ThreatInfo[] {
    return Object.values(bot.entities)
        .filter(
            (e: any) => (e.type === "mob" || e.type === "hostile") && e.name,
        )
        .map((e: any) => {
            const distance = bot.entity.position.distanceTo(e.position);
            const baseThreat =
                config.THREAT_WEIGHTS[e.name?.toLowerCase() || ""] || 5;
            const threatScore = baseThreat * (10 / Math.max(distance, 1));

            return {
                id: e.id!,
                name: e.name || "unknown",
                distance: parseFloat(distance.toFixed(1)),
                threatScore: Math.round(threatScore),
                position: { x: e.position.x, y: e.position.y, z: e.position.z },
            };
        })
        .sort((a, b) => b.threatScore - a.threatScore);
}

function computeSafeRetreat(threats: models.ThreatInfo[]) {
    let cx = 0,
        cz = 0,
        totalWeight = 0;

    for (const threat of threats) {
        cx += threat.position.x * threat.threatScore;
        cz += threat.position.z * threat.threatScore;
        totalWeight += threat.threatScore;
    }

    if (totalWeight === 0) {
        return {
            x: bot.entity.position.x + (Math.random() - 0.5) * 20,
            z: bot.entity.position.z + (Math.random() - 0.5) * 20,
        };
    }

    cx /= totalWeight;
    cz /= totalWeight;

    let dx = bot.entity.position.x - cx;
    let dz = bot.entity.position.z - cz;
    const len = Math.sqrt(dx * dx + dz * dz) || 1;

    return {
        x: bot.entity.position.x + (dx / len) * 20,
        z: bot.entity.position.z + (dz / len) * 20,
    };
}

function triggerReflexRetreat(threats: models.ThreatInfo[]) {
    const primaryThreat = threats[0];
    const cause =
        primaryThreat?.name || (bot.health < 6 ? "low_health" : "unknown");

    if (config.DEBUG_CHAT) {
        bot.chat(
            `Reflex: Evading ${cause}! (Health: ${Math.round(bot.health)})`,
        );
    }

    if (taskAbortController) taskAbortController.abort();

    reflexActive = true;
    stopMovement();

    client.sendEvent("panic_retreat", "evasion", "", cause, Date.now());

    const safePos = computeSafeRetreat(threats);

    log.warn("Reflex retreat triggered", {
        cause,
        health: bot.health,
        safe_x: Math.round(safePos.x),
        safe_z: Math.round(safePos.z),
    });

    (bot as any).pathfinder.setGoal(
        new goals.GoalNear(safePos.x, bot.entity.position.y, safePos.z, 2),
    );

    if (reflexTimeout) clearTimeout(reflexTimeout);

    reflexTimeout = setTimeout(() => {
        log.debug("Reflex retreat timeout elapsed; clearing reflex state");
        stopMovement();
        clearReflexState();
    }, 8000);
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

    if (taskAbortController) taskAbortController.abort();
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
                getThreats,
                computeSafeRetreat,
                stopMovement,
            ),
            timeoutPromise,
        ]);

        if (!signal.aborted) completeTask("task_completed");
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
        if (taskAbortController?.signal === signal) taskAbortController = null;
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

    if (config.ENABLE_VIEWER) {
        try {
            viewer(bot, {
                port: config.VIEWER_PORT,
                firstPerson: true,
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

bot.on("goal_reached", () => {
    if (reflexActive && currentTask === null) {
        log.debug("Reflex retreat goal reached");
        clearReflexState();
    }
});

bot.on("death", () => {
    log.warn("Bot died; resetting local execution state");
    resetExecutionState();
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
// SYSTEMS LOOPS
// ==========================================

bot.on("physicTick", () => {
    const now = Date.now();
    const threats = getThreats();

    if (
        (threats.length > 0 && threats[0]!.threatScore > 50) ||
        bot.health < 6
    ) {
        if (now - lastReflexTime > 2000) {
            triggerReflexRetreat(threats);
            lastReflexTime = now;
        }
    }
});

setInterval(() => {
    const state = {
        health: Math.round(bot.health),
        food: Math.round(bot.food),
        position: {
            x: Math.round(bot.entity.position.x),
            y: Math.round(bot.entity.position.y),
            z: Math.round(bot.entity.position.z),
        },
        threats: getThreats()
            .slice(0, 3)
            .map((t) => ({ name: t.name })),
        inventory: bot.inventory
            .items()
            .map((i) => ({ name: i.name, count: i.count })),
    };

    client.sendState(state);
    awaitingCommand = true;
}, 2000);
