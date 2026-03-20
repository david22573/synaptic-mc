import * as dotenv from "dotenv";
dotenv.config();

import pkg from "mineflayer-pathfinder";
const { pathfinder, Movements, goals } = pkg;
import mineflayer from "mineflayer";
import WebSocket from "ws";
import { mineflayer as viewer } from "prismarine-viewer";

// Feature Flags
const ENABLE_VIEWER = process.env.ENABLE_VIEWER === "true";
const VIEWER_PORT = parseInt(process.env.VIEWER_PORT || "3000", 10);
const DEBUG_CHAT = process.env.DEBUG_CHAT === "true";

const WS_URL = "ws://localhost:8080/ws";
const bot = mineflayer.createBot({
    host: "localhost",
    port: 25565,
    username: "CraftBot",
    version: "1.19",
});

bot.loadPlugin(pathfinder);

// Structured logger
const log = {
    info: (msg: string, meta: any = {}) =>
        console.log(
            JSON.stringify({
                level: "INFO",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),
    error: (msg: string, meta: any = {}) =>
        console.error(
            JSON.stringify({
                level: "ERROR",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),
    debug: (msg: string, meta: any = {}) => {
        if (process.env.DEBUG === "true")
            console.log(JSON.stringify({ level: "DEBUG", msg, ...meta }));
    },
};

interface ActiveTask {
    id: string;
    action: string;
    target: { type: string; name: string };
    startTime: number;
}

let ws: WebSocket;
let currentTask: ActiveTask | null = null;
let isBusy = false;
let reflexTimeout: NodeJS.Timeout | null = null;
let lastReflexTime = 0;
let taskAbortController: AbortController | null = null;

const TASK_TIMEOUTS: Record<string, number> = {
    attack: 20000,
    retreat: 15000,
    mine: 20000,
    idle: 3000,
};

function connectControlPlane() {
    ws = new WebSocket(WS_URL);

    ws.on("open", () => log.info("Connected to Go Control Plane"));

    ws.on("message", (data: Buffer) => {
        try {
            const msg = JSON.parse(data.toString());
            if (msg.type === "command") {
                executeDecision(msg.payload);
            } else if (msg.type === "planning_error" || msg.type === "noop") {
                log.debug(`Control plane unlocked bot: ${msg.type}`);
                isBusy = false;
            }
        } catch (err) {
            log.error("Failed to parse message", { err });
            isBusy = false;
        }
    });

    ws.on("close", () => {
        log.error("Disconnected. Retrying in 5s...");
        isBusy = false;
        setTimeout(connectControlPlane, 5000);
    });

    ws.on("error", (err) => log.error("WS Error", { err }));
}

function sendEvent(
    event: string,
    actionStr: string,
    commandId: string = "",
    cause: string = "",
    startTime: number = 0,
) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        const duration_ms =
            startTime > 0 ? Math.round(performance.now() - startTime) : 0;
        ws.send(
            JSON.stringify({
                type: "event",
                payload: {
                    event,
                    action: actionStr,
                    command_id: commandId,
                    cause,
                    duration_ms,
                },
            }),
        );
    }
}

const THREAT_WEIGHTS: Record<string, number> = {
    warden: 1000,
    creeper: 100,
    skeleton: 20,
    zombie: 10,
    spider: 10,
};

function getThreats() {
    return Object.values(bot.entities)
        .filter((e) => (e.type === "mob" || e.type === "hostile") && e.name)
        .map((e) => {
            const distance = bot.entity.position.distanceTo(e.position);
            const baseThreat = THREAT_WEIGHTS[e.name?.toLowerCase() || ""] || 5;
            const threatScore = baseThreat * (10 / Math.max(distance, 1));
            return {
                id: e.id,
                name: e.name || "unknown",
                distance: parseFloat(distance.toFixed(1)),
                threatScore: Math.round(threatScore),
                position: e.position,
            };
        })
        .sort((a, b) => b.threatScore - a.threatScore);
}

function computeSafeRetreat(threats: any[]) {
    let cx = 0,
        cz = 0,
        totalWeight = 0;
    for (const t of threats) {
        cx += t.position.x * t.threatScore;
        cz += t.position.z * t.threatScore;
        totalWeight += t.threatScore;
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
    let len = Math.sqrt(dx * dx + dz * dz) || 1;

    return {
        x: bot.entity.position.x + (dx / len) * 20,
        z: bot.entity.position.z + (dz / len) * 20,
    };
}

function triggerReflexRetreat(threats: any[]) {
    if (DEBUG_CHAT) bot.chat("Reflex triggered: Evading!");
    if (taskAbortController) taskAbortController.abort();

    const primaryThreat = threats[0];
    const startTime = performance.now();
    sendEvent("panic_retreat", "evasion", "", primaryThreat.name, startTime);
    isBusy = true;

    const safePos = computeSafeRetreat(threats);
    bot.pathfinder.setGoal(
        new goals.GoalNear(safePos.x, bot.entity.position.y, safePos.z, 2),
    );

    if (reflexTimeout) clearTimeout(reflexTimeout);
    reflexTimeout = setTimeout(() => {
        bot.clearControlStates();
        bot.pathfinder.setGoal(null);
        isBusy = false;
    }, 8000);
}

function completeTask(
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
) {
    if (!currentTask) return;
    sendEvent(
        status,
        `${currentTask.action} ${currentTask.target?.name}`,
        currentTask.id,
        "",
        currentTask.startTime,
    );
    currentTask = null;
    isBusy = false;
}

// [runTask logic remains identical to Phase 4]
async function runTask(decision: any, signal: AbortSignal): Promise<void> {
    /* ... */
}

async function executeDecision(decision: any) {
    log.info("Executing Decision", {
        action: decision.action,
        target: decision.target?.name,
        id: decision.id,
    });

    if (taskAbortController) taskAbortController.abort();
    taskAbortController = new AbortController();
    const signal = taskAbortController.signal;

    currentTask = {
        id: decision.id,
        action: decision.action,
        target: decision.target,
        startTime: performance.now(),
    };
    isBusy = true;

    bot.clearControlStates();
    bot.pathfinder.stop();

    sendEvent(
        "task_started",
        `${decision.action} ${decision.target?.name}`,
        decision.id,
        "",
        currentTask.startTime,
    );

    const timeoutMs = TASK_TIMEOUTS[decision.action] || 10000;
    let timeoutId: NodeJS.Timeout;
    const timeoutPromise = new Promise((_, reject) => {
        timeoutId = setTimeout(() => reject(new Error("timeout")), timeoutMs);
    });

    try {
        await Promise.race([runTask(decision, signal), timeoutPromise]);
        if (!signal.aborted) completeTask("task_completed");
    } catch (err: any) {
        if (signal.aborted || err.message === "aborted") {
            completeTask("task_aborted");
        } else {
            log.error("Task Failed", {
                action: decision.action,
                error: err.message,
            });
            completeTask("task_failed");
        }
    } finally {
        clearTimeout(timeoutId!);
        if (taskAbortController?.signal === signal) taskAbortController = null;
    }
}

bot.once("spawn", () => {
    log.info("Bot spawned");
    bot.pathfinder.setMovements(new Movements(bot));
    connectControlPlane();

    if (ENABLE_VIEWER) {
        try {
            viewer(bot, {
                port: VIEWER_PORT,
                firstPerson: true,
                viewDistance: 2,
            });
            log.info(`Prismarine viewer started on port ${VIEWER_PORT}`);
        } catch (err) {
            log.error("Viewer failed to start", { err });
        }
    }
});

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
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    if (isBusy) return;

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

    ws.send(JSON.stringify({ type: "state", payload: state }));
}, 2000);
