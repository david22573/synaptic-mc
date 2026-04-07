// js/lib/movement/navigator.ts
import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import { ProgressTracker } from "./progress.js";
import { straightLineMove, randomWalk } from "./recovery.js";
import { moveToGoal } from "../tasks/utils.js";
import { log } from "../logger.js";
import { ExecutionError } from "../tasks/primitives.js";

export interface NavOpts {
    timeoutMs?: number;
    signal?: AbortSignal;
    stopMovement?: () => void;
    maxRetries?: number;
}

export async function navigateWithFallbacks(
    bot: Bot,
    goal: any,
    opts: NavOpts,
): Promise<void> {
    if (!bot.entity || !bot.entity.position) {
        throw new ExecutionError("Bot not spawned", "BLOCKED", 0);
    }

    const maxRetries = opts.maxRetries ?? 3;
    let attempts = 0;

    const targetVec = new Vec3(
        goal.x || bot.entity.position.x,
        goal.y || bot.entity.position.y,
        goal.z || bot.entity.position.z,
    );

    const tracker = new ProgressTracker(bot, targetVec);

    try {
        while (attempts < maxRetries) {
            if (opts.signal?.aborted) {
                throw new ExecutionError(
                    "aborted",
                    "aborted",
                    tracker.getProgress(bot),
                );
            }

            try {
                // Phase 6: Micro-hesitation before initiating a new path search
                if (attempts === 0) {
                    await new Promise((resolve) =>
                        setTimeout(resolve, 150 + Math.random() * 300),
                    );
                }

                // Strategy 1: Pathfinder
                await moveToGoal(bot, goal, {
                    timeoutMs: opts.timeoutMs ?? 15000,
                    dynamic: false, // Default to static for stability
                    signal: opts.signal,
                    stopMovement: opts.stopMovement,
                });
                return;
            } catch (err: any) {
                const msg = err.message || "";
                if (msg === "aborted" || opts.signal?.aborted) {
                    throw new ExecutionError(
                        "aborted",
                        "aborted",
                        tracker.getProgress(bot),
                    );
                }

                attempts++;
                log.warn(
                    `Pathfinder failed (attempt ${attempts}/${maxRetries}). Escalating strategy.`,
                    { error: msg },
                );

                const progress = tracker.getProgress(bot);

                // Strategy 2: Straight-line shove
                if (attempts === 1) {
                    log.info("Strategy 2: Straight-line movement fallback");
                    await straightLineMove(
                        bot,
                        { x: targetVec.x, z: targetVec.z },
                        3000,
                    );
                    continue;
                }

                // Strategy 3: Random Walk
                if (attempts === 2) {
                    log.info("Strategy 3: Random walk fallback");
                    await randomWalk(bot, 3000);
                    continue;
                }

                // Exhausted
                log.info("Movement strategies exhausted. Bubbling failure.", {
                    progress,
                });
                throw new ExecutionError("pathing_failed", "blocked", progress);
            }
        }
    } finally {
        bot.clearControlStates();
    }
}
