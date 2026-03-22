// js/index.ts
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

// --- FIX 1: Prevent known 1.19 packet parsing glitches from spamming/crashing ---
process.on("uncaughtException", (err: Error) => {
    if (
        err.name === "PartialReadError" ||
        err.message.includes("Read error for undefined") ||
        err.message.includes("Missing characters in string")
    ) {
        return; // Silently ignore to keep the event loop clean
    }
    log.error("Fatal uncaught exception", {
        err: err.message,
        stack: err.stack,
    });
    process.exit(1);
});

let currentTask: models.ActiveTask | null = null;
let taskAbortController: AbortController | null = null;
let bot: mineflayer.Bot;
let client: ControlPlaneClient;
let survival: SurvivalSystem;
let runtimeConfig: config.Config;
let lastDeathMessage: string = "unknown causes";

// --- FIX 2: State Cache to prevent Event Loop blocking ---
let lastBlockSearchTime = 0;
let cachedHasBed = false;
let cachedHasTable = false;

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
                setTimeout(() => r(new Error("timeout")), timeoutMs),
            ),
        ]);
        if (!signal.aborted) completeTask(activeTask, "task_completed");
    } catch (err: any) {
        if (!signal.aborted)
            completeTask(activeTask, "task_failed", err.message);
    } finally {
        stopMovement();
    }
}

function pushState() {
    if (!bot?.entity || !client) return;

    const now = Date.now();
    // Only run expensive 32-block radius searches every 5 seconds (not every 2 seconds)
    if (now - lastBlockSearchTime > 5000) {
        cachedHasBed = !!bot.findBlock({
            matching: (b) => b?.name.includes("bed"),
            maxDistance: 32,
        });
        cachedHasTable = !!bot.findBlock({
            matching: (b) => b?.name === "crafting_table",
            maxDistance: 32,
        });
        lastBlockSearchTime = now;
    }

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
        has_bed_nearby: cachedHasBed,
        has_crafting_table_nearby: cachedHasTable,
        inventory: bot.inventory
            .items()
            .map((i) => ({ name: i.name, count: i.count })),
    });
}

async function bootstrap() {
    runtimeConfig = await config.loadConfig();

    bot = mineflayer.createBot({
        host: "0.0.0.0",
        port: 25565,
        username: "CraftBot",
        version: "1.19",
    });

    bot.loadPlugin(pathfinder);

    client = new ControlPlaneClient(runtimeConfig.ws_url, {
        onCommand: (d) => void executeDecision(d),
        onUnlock: () => abortActiveTask("unlock"),
    });

    survival = new SurvivalSystem(bot, client, {
        onInterrupt: (r) => {
            if (r === "panic_flee") abortActiveTask("survival_panic");
        },
        stopMovement: () => stopMovement(),
    });

    bot.on("error", (err: Error) => {
        if (
            err.name === "PartialReadError" ||
            err.message.includes("Read error for undefined") ||
            err.message.includes("Missing characters in string")
        ) {
            return; // Suppress internal Mineflayer parser errors
        }
        log.warn("Mineflayer bot emitted error", { err: err.message });
    });

    bot.on("spawn", () => {
        const movements = new Movements(bot);
        movements.canDig = true;
        movements.allow1by1towers = true;
        movements.allowParkour = true;
        movements.allowSprinting = true;
        // FIX: Dynamically resolve 1.19 liquid IDs instead of legacy 9/10
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
            survival.start();
            if (runtimeConfig.enable_viewer) {
                viewer(bot, {
                    port: runtimeConfig.viewer_port,
                    firstPerson: true,
                    viewDistance: 4,
                });
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
    setInterval(pushState, 2000);
}

bootstrap().catch((err) => log.error("Fatal startup", { err }));
