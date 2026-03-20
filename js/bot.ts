import * as dotenv from "dotenv";
import * as fs from "fs";
import * as path from "path";

// ==========================================
// LOGGER
// ==========================================

interface Logger {
    info: (msg: string, meta?: Record<string, unknown>) => void;
    warn: (msg: string, meta?: Record<string, unknown>) => void;
    error: (msg: string, meta?: Record<string, unknown>) => void;
    debug: (msg: string, meta?: Record<string, unknown>) => void;
}

const log: Logger = {
    info: (msg, meta = {}) =>
        console.log(
            JSON.stringify({
                level: "INFO",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),

    warn: (msg, meta = {}) =>
        console.warn(
            JSON.stringify({
                level: "WARN",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),

    error: (msg, meta = {}) =>
        console.error(
            JSON.stringify({
                level: "ERROR",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),

    debug: (msg, meta = {}) => {
        if (!DEBUG) return;
        console.log(
            JSON.stringify({
                level: "DEBUG",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        );
    },
};

function loadEnv(): string {
    const candidates = [
        path.resolve(process.cwd(), ".env"),
        path.resolve(process.cwd(), "../.env"),
    ];

    for (const envPath of candidates) {
        if (fs.existsSync(envPath)) {
            dotenv.config({ path: envPath });
            return envPath;
        }
    }

    dotenv.config();
    return "default-dotenv-lookup";
}

const loadedEnvPath = loadEnv();

import pkg from "mineflayer-pathfinder";
const { pathfinder, Movements, goals } = pkg;
import mineflayer from "mineflayer";
import WebSocket from "ws";
import { mineflayer as viewer } from "prismarine-viewer";

// ==========================================
// CONFIG
// ==========================================

const ENABLE_VIEWER = process.env.ENABLE_VIEWER === "true";
const VIEWER_PORT = parseInt(process.env.VIEWER_PORT || "3000", 10);
const DEBUG_CHAT = process.env.DEBUG_CHAT === "true";
const DEBUG = process.env.DEBUG === "true";

const WS_URL = "ws://localhost:8080/ws";

const bot = mineflayer.createBot({
    host: "localhost",
    port: 25565,
    username: "CraftBot",
    version: "1.19",
});

bot.loadPlugin(pathfinder);

// ==========================================
// TYPES
// ==========================================

interface DecisionTarget {
    type: string;
    name: string;
}

interface IncomingDecision {
    id: string;
    action: string;
    target: DecisionTarget;
    rationale?: string;
}

interface ActiveTask {
    id: string;
    action: string;
    target: DecisionTarget;
    startTime: number;
}

interface ThreatInfo {
    id: number;
    name: string;
    distance: number;
    threatScore: number;
    position: {
        x: number;
        y: number;
        z: number;
    };
}

// ==========================================
// STATE
// ==========================================

let ws: WebSocket;
let currentTask: ActiveTask | null = null;
let awaitingCommand = false;
let reflexActive = false;
let reflexTimeout: NodeJS.Timeout | null = null;
let lastReflexTime = 0;
let taskAbortController: AbortController | null = null;
let isBusy = false;

const TASK_TIMEOUTS: Record<string, number> = {
    attack: 20000,
    retreat: 15000,
    mine: 20000,
    idle: 3000,
};

const THREAT_WEIGHTS: Record<string, number> = {
    warden: 1000,
    creeper: 100,
    skeleton: 20,
    zombie: 10,
    spider: 10,
};

// ==========================================
// STATE HELPERS
// ==========================================

function updateBusyState() {
    isBusy = reflexActive || awaitingCommand || currentTask !== null;
}

function taskLabel(task: Pick<ActiveTask, "action" | "target">) {
    return `${task.action} ${task.target?.name || "none"}`.trim();
}

function stopMovement() {
    try {
        bot.clearControlStates();
    } catch {}

    try {
        bot.pathfinder.setGoal(null);
    } catch {}

    try {
        bot.pathfinder.stop();
    } catch {}
}

function clearReflexState() {
    reflexActive = false;

    if (reflexTimeout) {
        clearTimeout(reflexTimeout);
        reflexTimeout = null;
    }

    updateBusyState();
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
    updateBusyState();
}

// ==========================================
// WEBSOCKET
// ==========================================

function connectControlPlane() {
    ws = new WebSocket(WS_URL);

    ws.on("open", () => {
        log.info("Connected to Go Control Plane", {
            ws_url: WS_URL,
            env_path: loadedEnvPath,
            enable_viewer: ENABLE_VIEWER,
            viewer_port: VIEWER_PORT,
        });
    });

    ws.on("message", (data: Buffer) => {
        try {
            const msg = JSON.parse(data.toString());

            if (msg.type === "command") {
                awaitingCommand = false;
                updateBusyState();
                void executeDecision(msg.payload as IncomingDecision);
                return;
            }

            if (msg.type === "planning_error" || msg.type === "noop") {
                log.debug("Control plane unlocked bot", {
                    type: msg.type,
                    payload: msg.payload,
                });
                awaitingCommand = false;
                updateBusyState();
            }
        } catch (err) {
            log.error("Failed to parse control-plane message", {
                err: err instanceof Error ? err.message : String(err),
            });
            awaitingCommand = false;
            updateBusyState();
        }
    });

    ws.on("close", () => {
        log.error("Disconnected from Control Plane. Retrying in 5s...");
        resetExecutionState();
        setTimeout(connectControlPlane, 5000);
    });

    ws.on("error", (err) => {
        log.error("WebSocket error", {
            err: err instanceof Error ? err.message : String(err),
        });
    });
}

function sendEvent(
    event: string,
    actionStr: string,
    commandId = "",
    cause = "",
    startTime = 0,
) {
    if (!ws || ws.readyState !== WebSocket.OPEN) {
        return;
    }

    const duration_ms = startTime > 0 ? Date.now() - startTime : 0;

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

// ==========================================
// THREATS / REFLEXES
// ==========================================

function getThreats(): ThreatInfo[] {
    return Object.values(bot.entities)
        .filter(
            (e: any) => (e.type === "mob" || e.type === "hostile") && e.name,
        )
        .map((e: any) => {
            const distance = bot.entity.position.distanceTo(e.position);
            const baseThreat = THREAT_WEIGHTS[e.name?.toLowerCase() || ""] || 5;
            const threatScore = baseThreat * (10 / Math.max(distance, 1));

            return {
                id: e.id!,
                name: e.name || "unknown",
                distance: parseFloat(distance.toFixed(1)),
                threatScore: Math.round(threatScore),
                position: {
                    x: e.position.x,
                    y: e.position.y,
                    z: e.position.z,
                },
            };
        })
        .sort((a, b) => b.threatScore - a.threatScore);
}

function computeSafeRetreat(threats: ThreatInfo[]) {
    let cx = 0;
    let cz = 0;
    let totalWeight = 0;

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

    dx /= len;
    dz /= len;

    return {
        x: bot.entity.position.x + dx * 20,
        z: bot.entity.position.z + dz * 20,
    };
}

function triggerReflexRetreat(threats: ThreatInfo[]) {
    const primaryThreat = threats[0];
    const cause =
        primaryThreat?.name || (bot.health < 6 ? "low_health" : "unknown");

    if (DEBUG_CHAT) {
        bot.chat(
            `Reflex: Evading ${cause}! (Health: ${Math.round(bot.health)})`,
        );
    }

    if (taskAbortController) {
        taskAbortController.abort();
    }

    reflexActive = true;
    updateBusyState();
    stopMovement();

    const startTime = Date.now();
    sendEvent("panic_retreat", "evasion", "", cause, startTime);

    const safePos = computeSafeRetreat(threats);

    log.warn("Reflex retreat triggered", {
        cause,
        health: bot.health,
        safe_x: Math.round(safePos.x),
        safe_z: Math.round(safePos.z),
    });

    bot.pathfinder.setGoal(
        new goals.GoalNear(safePos.x, bot.entity.position.y, safePos.z, 2),
    );

    if (reflexTimeout) {
        clearTimeout(reflexTimeout);
    }

    reflexTimeout = setTimeout(() => {
        log.debug("Reflex retreat timeout elapsed; clearing reflex state");
        stopMovement();
        clearReflexState();
    }, 8000);
}

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
// TASK HELPERS
// ==========================================

function waitForMs(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
        if (signal.aborted) {
            reject(new Error("aborted"));
            return;
        }

        const timer = setTimeout(() => {
            cleanup();
            resolve();
        }, ms);

        const onAbort = () => {
            clearTimeout(timer);
            cleanup();
            reject(new Error("aborted"));
        };

        const cleanup = () => {
            signal.removeEventListener("abort", onAbort);
        };

        signal.addEventListener("abort", onAbort, { once: true });
    });
}

function moveToGoal(
    goal: any,
    signal: AbortSignal,
    timeoutMs: number,
): Promise<void> {
    return new Promise((resolve, reject) => {
        if (signal.aborted) {
            reject(new Error("aborted"));
            return;
        }

        let settled = false;

        const cleanup = () => {
            clearTimeout(timer);
            signal.removeEventListener("abort", onAbort);
            bot.removeListener("goal_reached", onGoalReached);
        };

        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;
            cleanup();

            if (err) {
                reject(err);
                return;
            }

            resolve();
        };

        const onAbort = () => {
            stopMovement();
            finish(new Error("aborted"));
        };

        const onGoalReached = () => {
            stopMovement();
            finish();
        };

        const timer = setTimeout(() => {
            stopMovement();
            finish(new Error("timeout"));
        }, timeoutMs);

        signal.addEventListener("abort", onAbort, { once: true });
        bot.once("goal_reached", onGoalReached);
        bot.pathfinder.setGoal(goal);
    });
}

function findNearestBlockByName(blockName: string) {
    return bot.findBlock({
        maxDistance: 32,
        matching: (block: any) => block?.name === blockName,
    });
}

// ==========================================
// TASK LIFECYCLE
// ==========================================

function completeTask(
    status:
        | "task_completed"
        | "task_failed"
        | "task_aborted" = "task_completed",
) {
    if (!currentTask) {
        updateBusyState();
        return;
    }

    sendEvent(
        status,
        taskLabel(currentTask),
        currentTask.id,
        "",
        currentTask.startTime,
    );

    currentTask = null;
    updateBusyState();
}

async function runTask(
    decision: IncomingDecision,
    signal: AbortSignal,
): Promise<void> {
    switch (decision.action) {
        case "idle": {
            await waitForMs(1500, signal);
            return;
        }

        case "retreat": {
            const threats = getThreats();
            const safePos = computeSafeRetreat(threats);

            await moveToGoal(
                new goals.GoalNear(
                    safePos.x,
                    bot.entity.position.y ?? 64,
                    safePos.z,
                    2,
                ),
                signal,
                TASK_TIMEOUTS.retreat!,
            );

            return;
        }

        case "attack": {
            const targetName = decision.target?.name;
            if (!targetName || targetName === "none") {
                throw new Error("missing attack target");
            }

            const attackStartedAt = Date.now();
            let hasSeenTarget = false;
            let lastSeenAt = 0;

            while (Date.now() - attackStartedAt < TASK_TIMEOUTS.attack!) {
                if (signal.aborted) {
                    throw new Error("aborted");
                }

                const targetEntity = bot.nearestEntity(
                    (e: any) => e.name === targetName && e.isValid,
                );

                if (!targetEntity) {
                    if (hasSeenTarget && Date.now() - lastSeenAt > 1500) {
                        stopMovement();
                        return;
                    }

                    if (!hasSeenTarget && Date.now() - attackStartedAt > 3000) {
                        throw new Error(`target not found: ${targetName}`);
                    }

                    await waitForMs(250, signal);
                    continue;
                }

                hasSeenTarget = true;
                lastSeenAt = Date.now();

                bot.pathfinder.setGoal(
                    new goals.GoalFollow(targetEntity, 2),
                    true,
                );

                const dist = bot.entity.position.distanceTo(
                    targetEntity.position,
                );

                if (!targetEntity || !targetEntity.position) {
                    throw new Error(
                        `Target ${targetName} is no longer in range or valid.`,
                    );
                }

                if (dist < 3.2) {
                    try {
                        await bot.lookAt(
                            targetEntity.position.offset(
                                0,
                                targetEntity.height ?? 1.6,
                                0,
                            ),
                            true,
                        );
                    } catch {}

                    try {
                        bot.attack(targetEntity);
                    } catch {}
                }

                await waitForMs(450, signal);
            }

            throw new Error("timeout");
        }

        case "mine": {
            const targetBlockName = decision.target?.name;
            if (!targetBlockName || targetBlockName === "none") {
                throw new Error("missing mine target");
            }

            const block = findNearestBlockByName(targetBlockName);
            if (!block) {
                throw new Error(`block not found: ${targetBlockName}`);
            }

            await moveToGoal(
                new goals.GoalNear(
                    block.position.x,
                    block.position.y,
                    block.position.z,
                    1,
                ),
                signal,
                12000,
            );

            if (signal.aborted) {
                throw new Error("aborted");
            }

            if (!bot.canDigBlock(block)) {
                throw new Error(`cannot dig block: ${targetBlockName}`);
            }

            await bot.dig(block);
            return;
        }

        default:
            throw new Error(`unsupported action: ${decision.action}`);
    }
}

async function executeDecision(decision: IncomingDecision) {
    if (!decision?.action) {
        log.error("Received malformed command", { decision });
        awaitingCommand = false;
        updateBusyState();
        return;
    }

    log.info("Executing decision", {
        id: decision.id,
        action: decision.action,
        target_type: decision.target?.type,
        target_name: decision.target?.name,
        rationale: decision.rationale,
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
    updateBusyState();

    stopMovement();

    sendEvent(
        "task_started",
        taskLabel(currentTask),
        decision.id,
        "",
        currentTask.startTime,
    );

    const timeoutMs = TASK_TIMEOUTS[decision.action] || 10000;

    let timeoutId: NodeJS.Timeout | null = null;
    const timeoutPromise = new Promise<never>((_, reject) => {
        timeoutId = setTimeout(() => reject(new Error("timeout")), timeoutMs);
    });

    try {
        await Promise.race([runTask(decision, signal), timeoutPromise]);

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
                target: decision.target?.name,
                error: message,
            });
            completeTask("task_failed");
        }
    } finally {
        if (timeoutId) {
            clearTimeout(timeoutId);
        }

        stopMovement();

        if (taskAbortController?.signal === signal) {
            taskAbortController = null;
        }

        updateBusyState();
    }
}

// ==========================================
// BOT LIFECYCLE
// ==========================================

bot.on("login", () => {
    log.info("Bot logged in");
});

bot.once("spawn", () => {
    log.info("Bot spawned", {
        env_path: loadedEnvPath,
        cwd: process.cwd(),
        enable_viewer: ENABLE_VIEWER,
        viewer_port: VIEWER_PORT,
    });

    bot.pathfinder.setMovements(new Movements(bot));
    connectControlPlane();

    if (ENABLE_VIEWER) {
        try {
            viewer(bot, {
                port: VIEWER_PORT,
                firstPerson: true,
                viewDistance: 2,
            });

            log.info("Prismarine viewer started", {
                url: `http://localhost:${VIEWER_PORT}`,
                port: VIEWER_PORT,
            });
        } catch (err) {
            log.error("Viewer failed to start", {
                err: err instanceof Error ? err.message : String(err),
            });
        }
    } else {
        log.info("Prismarine viewer disabled", {
            enable_viewer: ENABLE_VIEWER,
            env_path: loadedEnvPath,
        });
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

bot.on("kicked", (reason: unknown) => {
    log.error("Bot was kicked", { reason });
});

bot.on("end", (reason: unknown) => {
    log.error("Bot connection ended", { reason });
});

bot.on("error", (err: unknown) => {
    log.error("Bot error", {
        err: err instanceof Error ? err.message : String(err),
    });
});

// ==========================================
// HIGH-FREQUENCY REFLEX LOOP
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

// ==========================================
// LOW-FREQUENCY STATE SYNC
// ==========================================

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
    awaitingCommand = true;
    updateBusyState();
}, 2000);
