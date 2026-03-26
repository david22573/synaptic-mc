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
import { getPOIs } from "./lib/utils/perception.js";

const { pathfinder, Movements } = pkg;

let viewerStarted = false;
const isIgnorableError = (err: Error | any) => {
    const msg = err?.message || "";
    const name = err?.name || "";
    return (
        name === "PartialReadError" ||
        msg.includes("Read error for undefined") ||
        msg.includes("Missing characters in string")
    );
};

process.on("uncaughtException", (err: Error) => {
    if (isIgnorableError(err)) return;
    log.error("Fatal uncaught exception", {
        err: err.message,
        stack: err.stack,
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
let client: ControlPlaneClient;
let survival: SurvivalSystem;
let runtimeConfig: config.Config;
let lastDeathMessage: string = "unknown causes";

let lastStateSig = "";
let lastStatePushTime = 0;
let reconnectAttempt = 1;
let stateInterval: NodeJS.Timeout | null = null;

function stopMovement() {
    if (!bot) return;
    bot.clearControlStates();
    if (bot.pathfinder) bot.pathfinder.setGoal(null);
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
    client.sendEvent(
        status,
        `${task.action} ${task.target?.name || "none"}`,
        task.id,
        cause,
        task.startTime,
    );
    if (currentTask?.id === task.id) currentTask = null;
}

function abortActiveTask(reason: string) {
    if (currentTask) {
        if (taskAbortController) taskAbortController.abort(reason);
        completeTask(currentTask, "task_aborted", reason);
    }
}

function normalizeFailureCause(err: any): string {
    const msg = String(err?.message || err).toLowerCase();
    if (
        msg.includes("no path") ||
        msg.includes("path_fail") ||
        msg.includes("failed_to_reach") ||
        msg.includes("pathfinder_timeout")
    ) {
        return "PATH_FAILED";
    }
    if (
        msg.includes("timeout") ||
        msg.includes("fsm_global_timeout") ||
        msg.includes("furnace_timeout_or_lag")
    ) {
        return "TIMEOUT";
    }
    if (msg.includes("stuck")) {
        return "STUCK";
    }
    if (
        msg.includes("exhausted") ||
        msg.includes("no_") ||
        msg.includes("missing_") ||
        msg.includes("unknown_")
    ) {
        return "NO_BLOCKS";
    }

    return "UNKNOWN";
}

async function executeDecision(decision: models.IncomingDecision) {
    if (!decision?.action) return;
    if (survival?.isPanickingNow()) {
        if (decision.action === "retreat") {
            survival.reset();
        } else {
            client.sendEvent(
                "task_aborted",
                "lock",
                decision.id,
                "panic",
                Date.now(),
            );
            return;
        }
    }

    abortActiveTask("preempted");
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

    try {
        const timeouts = runtimeConfig.task_timeouts || config.TASK_TIMEOUTS;
        const timeoutMs = timeouts[decision.action] || 15000;

        await Promise.race([
            runTask(
                bot,
                decision,
                signal,
                timeouts,
                () => getThreats(bot),
                (t) => computeSafeRetreat(bot, t, 20),
                stopMovement,
            ),
            new Promise((_, r) =>
                setTimeout(() => {
                    if (taskAbortController)
                        taskAbortController.abort("timeout");
                    r(new Error("timeout"));
                }, timeoutMs),
            ),
        ]);

        if (!signal.aborted) completeTask(activeTask, "task_completed");
    } catch (err: any) {
        if (!signal.aborted) {
            const normalizedCause = normalizeFailureCause(err);
            completeTask(activeTask, "task_failed", normalizedCause);
        }
    } finally {
        stopMovement();
    }
}

function pushState() {
    if (!bot?.entity || !client) return;
    const sig = `${bot.health}|${bot.food}|${Math.round(bot.entity.position.x)},${Math.round(bot.entity.position.y)},${Math.round(bot.entity.position.z)}|${bot.inventory
        .items()
        .map((i) => `${i.name}:${i.count}`)
        .sort()
        .join(",")}`;
    const now = Date.now();

    if (sig === lastStateSig && now - lastStatePushTime < 5000) return;

    lastStateSig = sig;
    lastStatePushTime = now;

    client.sendState({
        health: Math.round(bot.health),
        food: Math.round(bot.food),
        time_of_day: bot.time.timeOfDay,
        position: {
            x: Math.round(bot.entity.position.x),
            y: Math.round(bot.entity.position.y),
            z: Math.round(bot.entity.position.z),
        },
        threats: getThreats(bot)
            .slice(0, 3)
            .map((t) => ({ name: t.name })),
        pois: getPOIs(bot),
        inventory: bot.inventory
            .items()
            .map((i) => ({ name: i.name, count: i.count })),
    });
}

async function connectWithRetry(maxAttempts = 10) {
    if (reconnectAttempt > maxAttempts) {
        log.error("Max reconnection attempts reached. Shutting down.");
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

    log.info(`Connecting bot (Attempt ${reconnectAttempt}/${maxAttempts})...`);
    bot = mineflayer.createBot({
        host: "0.0.0.0",
        port: 25565,
        username: "CraftBot",
        version: false,
    });
    bot.loadPlugin(pathfinder);

    if (!client) {
        client = new ControlPlaneClient(runtimeConfig.ws_url, {
            onCommand: (d) => void executeDecision(d),
            onUnlock: () => abortActiveTask("unlock"),
        });
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
        log.warn(`Bot disconnected. Reason: ${reason}`);
        abortActiveTask("bot_disconnected");
        survival.stop();

        const backoffMs = Math.min(1000 * Math.pow(2, reconnectAttempt), 30000);
        log.info(`Retrying in ${backoffMs / 1000}s...`);
        setTimeout(() => connectWithRetry(maxAttempts), backoffMs);
        reconnectAttempt++;
    });
    bot.on("spawn", () => {
        reconnectAttempt = 1;
        log.info("Bot spawned successfully.");
        const movements = new Movements(bot);
        movements.canDig = true;
        movements.allow1by1towers = true;
        movements.allowParkour = true;
        movements.allowSprinting = true;

        const water = bot.registry.blocksByName.water?.id;
        const lava = bot.registry.blocksByName.lava?.id;
        movements.liquids = new Set(
            [water, lava].filter((id) => id !== undefined) as number[],
        );
        movements.digCost = 1.5;

        const trashBlocks = ["dirt", "cobblestone", "netherrack", "stone"];
        movements.scafoldingBlocks = trashBlocks
            .map((name) => bot.registry.blocksByName[name]?.id)
            .filter((id) => id !== undefined);

        bot.pathfinder.setMovements(movements);
        bot.pathfinder.thinkTimeout = 10000;

        if (!client.isConnected()) {
            client.connect();
        }
        survival.start();
        if (runtimeConfig.enable_viewer) {
            try {
                if (viewerStarted) {
                    log.warn("Viewer already started, skipping new instance");
                    return;
                }
                viewer(bot, {
                    port: runtimeConfig.viewer_port,
                    firstPerson: true,
                    viewDistance: 4,
                });
                viewerStarted = true;
            } catch (err) {
                log.warn("Could not start viewer", { err });
            }
        }
    });
    bot.on("message", (msg) => {
        const text = msg.toString();
        if (text.includes(bot.username) && !text.startsWith("<"))
            lastDeathMessage = text;
    });
    bot.on("death", () => {
        log.warn(
            `Bot died. Cause: ${lastDeathMessage}. Notifying control plane.`,
        );
        abortActiveTask("bot_died");
        client.sendEvent("death", "death", "", lastDeathMessage, 0);
        lastDeathMessage = "unknown causes";
    });
    bot.on("health", pushState);

    if (stateInterval) clearInterval(stateInterval);
    stateInterval = setInterval(pushState, 2000);
}

async function bootstrap() {
    runtimeConfig = await config.loadConfig();
    await connectWithRetry();
}

bootstrap().catch((err) => log.error("Fatal startup", { err }));
