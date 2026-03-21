import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import * as models from "../models.js";

// External Handlers
import { handleGather } from "./handlers/gather.js";
import { handleCraft } from "./handlers/craft.js";
import { handleHunt } from "./handlers/hunt.js";
import { handleBuild } from "./handlers/build.js";
import { handleExplore } from "./handlers/explore.js";
import { handleSmelt } from "./handlers/smelt.js";

// Utils
import { escapeTree, moveToGoal, waitForMs } from "./utils.js";
import { normalizeDecision } from "./normalize.js";

const { goals } = pkg;

// ==========================================
// TASK EXECUTION
// ==========================================

export async function runTask(
    bot: Bot,
    rawDecision: models.IncomingDecision,
    signal: AbortSignal,
    timeouts: Record<string, number>,
    getThreats: () => models.ThreatInfo[],
    computeSafeRetreat: (threats: models.ThreatInfo[]) => {
        x: number;
        z: number;
    },
    stopMovement: () => void,
): Promise<void> {
    // 1. Normalize the decision targets centrally
    const decision = normalizeDecision(bot, rawDecision);

    // Construct the standard context bundle
    const taskCtx = {
        bot,
        decision,
        signal,
        timeouts,
        stopMovement,
    };

    switch (decision.action) {
        // --- DELEGATED FSM / MODULAR TASKS ---
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

        case "explore":
            await handleExplore(taskCtx);
            return;

        // --- INLINE FAST TASKS ---
        case "idle": {
            await waitForMs(1500, signal);
            return;
        }

        case "sleep": {
            await escapeTree(bot, signal);

            const bed = bot.findBlock({
                maxDistance: 32,
                matching: (block: any) => block?.name.includes("bed"),
            });

            if (!bed) {
                throw new Error("no bed found nearby");
            }

            await moveToGoal(
                bot,
                new goals.GoalNear(
                    bed.position.x,
                    bed.position.y,
                    bed.position.z,
                    1.5,
                ),
                { signal, timeoutMs: 20000, stopMovement, dynamic: false },
            );

            if (signal.aborted) throw new Error("aborted");

            try {
                await bot.sleep(bed);
            } catch (err) {
                const msg = err instanceof Error ? err.message : String(err);
                if (
                    msg.includes("It's not night") ||
                    msg.includes("can't sleep")
                ) {
                    return; // Normal day/night cycle rejection, not a failure
                }
                throw new Error(`sleep interaction failed: ${msg}`);
            }
            return;
        }

        case "retreat": {
            await escapeTree(bot, signal);

            const threats = getThreats();
            const safePos = computeSafeRetreat(threats);

            await moveToGoal(
                bot,
                new goals.GoalNearXZ(safePos.x, safePos.z, 2),
                {
                    signal,
                    timeoutMs: timeouts.retreat ?? 15000,
                    stopMovement,
                    dynamic: false,
                },
            );
            return;
        }

        default:
            throw new Error(`unsupported action: ${decision.action}`);
    }
}
