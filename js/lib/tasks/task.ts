import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import * as models from "../models.js";
import { type TaskContext } from "./registry.js";
import { log } from "../logger.js"; // Added for error logging

// Handlers
import { handleGather } from "./handlers/gather.js";
import { handleCraft } from "./handlers/craft.js";
import { handleHunt } from "./handlers/hunt.js";
import { handleBuild } from "./handlers/build.js";
import { handleExplore } from "./handlers/explore.js";
import { handleSmelt } from "./handlers/smelt.js";
import { handleMine } from "./handlers/mine.js";
import { handleFarm } from "./handlers/farm.js";
import { escapeTree, moveToGoal, waitForMs } from "./utils.js";
import { normalizeIntent } from "./normalize.js";
import { handleInteract } from "./handlers/interact.js";
import { handleStore } from "./handlers/store.js";
import { handleRetrieve } from "./handlers/retrieve.js";

const { goals } = pkg;

function calculateDynamicTimeout(
    intent: models.ActionIntent,
    bot: Bot,
    baseTimeouts: Record<string, number>,
): number {
    const baseTimeout = baseTimeouts[intent.action] || 15000;
    const invCount = bot.inventory.items().length;
    const healthFactor = Math.max(bot.health, 1) / 20;

    return baseTimeout * (1 + invCount * 0.02) * (2 - healthFactor);
}

export async function runTask(
    bot: Bot,
    rawIntent: models.ActionIntent,
    signal: AbortSignal,
    timeouts: Record<string, number>,
    getThreats: () => models.ThreatInfo[],
    computeSafeRetreat: (threats: models.ThreatInfo[]) => {
        x: number;
        z: number;
    },
    stopMovement: () => void,
): Promise<void> {
    const intent = normalizeIntent(bot, rawIntent);

    const dynamicTimeouts = { ...timeouts };
    dynamicTimeouts[intent.action] = calculateDynamicTimeout(
        intent,
        bot,
        timeouts,
    );

    const taskCtx: TaskContext = {
        bot,
        intent,
        signal,
        timeouts: dynamicTimeouts,
        getThreats,
        computeSafeRetreat,
        stopMovement,
    };

    if (signal.aborted) {
        throw new Error("aborted");
    }

    try {
        switch (intent.action) {
            case "hunt":
                await handleHunt(taskCtx);
                break;
            case "gather":
                await handleGather(taskCtx);
                break;
            case "craft":
                await handleCraft(taskCtx);
                break;
            case "build":
                await handleBuild(taskCtx);
                break;
            case "smelt":
                await handleSmelt(taskCtx);
                break;
            case "mine":
                await handleMine(taskCtx);
                break;
            case "farm":
                await handleFarm(taskCtx);
                break;
            case "explore":
                await handleExplore(taskCtx);
                break;
            case "store":
                await handleStore(taskCtx);
                break;
            case "retrieve":
                await handleRetrieve(taskCtx);
                break;
            case "eat": {
                const food = bot.inventory
                    .items()
                    .find((i) => i.name === intent.target.name);

                if (!food) throw new Error(`NO_FOOD: ${intent.target.name}`);

                try {
                    await bot.equip(food.type, "hand");
                    await bot.consume();
                } catch (err) {
                    throw new Error(
                        `CONSUME_FAILED: ${err instanceof Error ? err.message : String(err)}`,
                    );
                }
                break;
            }
            case "idle":
                await waitForMs(1500, signal);
                break;
            case "sleep": {
                await escapeTree(bot, signal);

                const bed = bot.findBlock({
                    maxDistance: 32,
                    matching: (b: any) => b?.name.includes("bed"),
                });

                if (!bed) throw new Error("no bed found");

                await moveToGoal(
                    bot,
                    new goals.GoalNear(
                        bed.position.x,
                        bed.position.y,
                        bed.position.z,
                        1.5,
                    ),
                    { signal, timeoutMs: 20000, stopMovement },
                );

                let onWake: (() => void) | undefined;
                let onAbort: (() => void) | undefined;

                try {
                    const wakePromise = new Promise<void>((resolve, reject) => {
                        onWake = () => resolve();
                        onAbort = () => reject(new Error("aborted"));

                        bot.on("wake", onWake);
                        signal.addEventListener("abort", onAbort, {
                            once: true,
                        });
                    });

                    const sleepPromise = bot.sleep(bed).then(() => wakePromise);
                    const timeoutPromise = new Promise<void>((resolve) =>
                        setTimeout(resolve, 12000),
                    );

                    await Promise.race([sleepPromise, timeoutPromise]);
                } finally {
                    if (onWake) bot.removeListener("wake", onWake);
                    if (onAbort) signal.removeEventListener("abort", onAbort);
                }

                break;
            }
            case "retreat": {
                await escapeTree(bot, signal);

                const pos = computeSafeRetreat(getThreats());
                await moveToGoal(bot, new goals.GoalNearXZ(pos.x, pos.z, 2), {
                    signal,
                    timeoutMs: 15000,
                    stopMovement,
                });

                await waitForMs(1000, signal);
                break;
            }
            case "interact":
                await handleInteract(taskCtx);
                break;
            case "mark_location":
            case "recall_location":
                await waitForMs(500, signal);
                break;
            default:
                throw new Error(`unsupported: ${intent.action}`);
        }
    } catch (err: any) {
        // Ensure errors are propagated to trigger the Anti-Stuck Reflex in index.ts
        stopMovement();
        log.error(`Task handler error in ${intent.action}`, {
            error: err.message,
            stack: err.stack,
        });
        throw err;
    }
}
