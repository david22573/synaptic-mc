import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import * as models from "../models.js";
import { type TaskContext } from "./registry.js";

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
import { handleRetrieve } from "./handlers/retrieve.js"; // New import

const { goals } = pkg;

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

    const taskCtx: TaskContext = {
        bot,
        intent,
        signal,
        timeouts,
        getThreats,
        computeSafeRetreat,
        stopMovement,
    };

    if (signal.aborted) {
        throw new Error("aborted");
    }

    switch (intent.action) {
        case "hunt":
            await handleHunt(taskCtx);
            return;
        case "gather":
            await handleGather(taskCtx);
            return;
        case "craft":
            await handleCraft(taskCtx);
            return;
        case "build":
            await handleBuild(taskCtx);
            return;
        case "smelt":
            await handleSmelt(taskCtx);
            return;
        case "mine":
            await handleMine(taskCtx);
            return;
        case "farm":
            await handleFarm(taskCtx);
            return;
        case "explore":
            await handleExplore(taskCtx);
            return;
        case "store":
            await handleStore(taskCtx);
            return;
        case "retrieve":
            await handleRetrieve(taskCtx);
            return;
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
            return;
        }
        case "idle":
            await waitForMs(1500, signal);
            return;
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
                    signal.addEventListener("abort", onAbort, { once: true });
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

            return;
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
            return;
        }
        case "interact":
            await handleInteract(taskCtx);
            return;
        case "mark_location":
        case "recall_location":
            await waitForMs(500, signal);
            return;
        default:
            throw new Error(`unsupported: ${intent.action}`);
    }
}
