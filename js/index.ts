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

const isIgnorableError = (err: Error | any): boolean => {
    if (!err) return false;
    const msg = String(err.message || err.stack || err);
    const name = String(err.name || "");
    return (
        name.includes("PartialReadError") ||
        msg.includes("Read error for undefined") ||
        msg.includes("Missing characters in string") ||
        msg.includes("protodef") ||
        (msg.includes("size is") && msg.includes("expected size")) ||
        msg.includes("Unexpected server response")
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
    ) {
        return;
    }
    originalConsoleError.apply(console, args);
};

const originalConsoleWarn = console.warn;
console.warn = (...args: any[]) => {
    const firstArg = args[0];
    if (
        typeof firstArg === "string" &&
        firstArg.includes("Read error for undefined")
    )
        return;
    originalConsoleWarn.apply(console, args);
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
let isShuttingDown = false;

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
        msg.includes("pathfinder_timeout")
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
        msg.includes("target_lost")
    )
        return "NO_ENTITY";

    if (msg.includes("no_pickaxe_equipped") || msg.includes("no_tool"))
        return "NO_TOOL";

    if (
        msg.includes("no_furnace_available") ||
        msg.includes("failed_to_reach_furnace")
    )
        return "NO_FURNACE";

    if (msg.includes("no_mature_")) return "NO_MATURE_CROP";

    if (
        msg.includes("missing_ingredients") ||
        msg.includes("missing_crafting_table")
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

async function executeIntent(intent: models.ActionIntent) {
    if (!intent?.action) return;
    if (isShuttingDown) return;

    if (survival?.isPanickingNow()) {
        if (intent.action === "retreat") {
            survival.reset();
        } else {
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

    stopMovement();
    const activeTask = currentTask;
    let timeoutId: NodeJS.Timeout | undefined;

    try {
        const timeouts = runtimeConfig.task_timeouts || config.TASK_TIMEOUTS;
        const timeoutMs = timeouts[intent.action] || 15000;

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

            pushState();
            if (activeTask)
                completeTask(activeTask, "task_failed", normalizedCause);
        }
    } finally {
        if (timeoutId) clearTimeout(timeoutId);
        stopMovement();
        if (currentTask?.id === activeTask?.id) currentTask = null;
        if (taskAbortController === localController) taskAbortController = null;
    }
}

function pushState() {
    if (!bot?.entity || !client) return;

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
        .join(",")}`;

    const now = Date.now();
    if (sig === lastStateSig && now - lastStatePushTime < 5000) return;

    lastStateSig = sig;
    lastStatePushTime = now;

    client.sendState({
        health: Math.round(bot.health),
        food: Math.round(bot.food),
        time_of_day: bot.time.timeOfDay,
        experience: bot.experience?.progress ?? 0,
        level: bot.experience?.level ?? 0,
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
        hotbar: hotbar,
        offhand: offhandItem
            ? { name: offhandItem.name, count: offhandItem.count }
            : null,
        active_slot: bot.quickBarSlot,
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

    if (!client) {
        client = new ControlPlaneClient(runtimeConfig.ws_url, {
            onCommand: (i) => void executeIntent(i),
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
        abortActiveTask("bot_disconnected");
        survival.stop();
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
        bot.pathfinder.thinkTimeout = 20000;

        bot.inventory.removeAllListeners("updateSlot");
        bot.inventory.on("updateSlot", pushState);

        if (!client.isConnected()) client.connect();

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
    });

    bot.on("message", (msg) => {
        const text = msg.toString();
        if (text.includes(bot.username) && !text.startsWith("<"))
            lastDeathMessage = text;
    });

    bot.on("death", () => {
        abortActiveTask("bot_died");
        client.sendEvent("death", "death", "", lastDeathMessage, 0);
        lastDeathMessage = "unknown causes";
    });

    bot.on("health", pushState);
    bot.on("experience", pushState);

    // @ts-ignore - Supress missing typing for this event in older versions
    bot.on("heldItemChanged", pushState);

    if (stateInterval) clearInterval(stateInterval);
    stateInterval = setInterval(pushState, 2000);
}

async function bootstrap() {
    runtimeConfig = await config.loadConfig();
    log.info("Bot configuration loaded", { ws_url: runtimeConfig.ws_url });
    await connectWithRetry();
}

process.on("SIGINT", () => {
    isShuttingDown = true;
    abortActiveTask("shutdown");
    if (bot) bot.quit();
    if (client?.isConnected()) {
        setTimeout(() => process.exit(0), 1000);
    } else {
        process.exit(0);
    }
});

bootstrap().catch((err) => {
    log.error("Fatal startup", {
        err: err instanceof Error ? err.message : String(err),
        stack: err?.stack,
    });
    process.exit(1);
});
