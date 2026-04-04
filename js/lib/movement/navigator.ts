// js/lib/movement/navigator.ts
import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import { ProgressTracker } from "./progress.js";
import { straightLineMove, jumpRecovery, randomWalk } from "./recovery.js";
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
    const maxRetries = opts.maxRetries ?? 3;
    let attempts = 0;

    const targetVec = new Vec3(
        goal.x || bot.entity.position.x,
        goal.y || bot.entity.position.y,
        goal.z || bot.entity.position.z,
    );

    const tracker = new ProgressTracker(bot, targetVec);

    const stuckMonitor = setInterval(() => {
        if (tracker.checkStuck(bot)) {
            log.warn(
                "STUCK DETECTED in navigator: Triggering quick jump recovery",
            );
            jumpRecovery(bot, 500);
        }
    }, 3000);

    // Phase 6: Humanization - Add natural looking camera noise
    // Breaks the perfect mechanical stare of the pathfinder.
    const noiseMonitor = setInterval(() => {
        if (!bot.pathfinder || !bot.pathfinder.isMoving()) return;
        const yawNoise = (Math.random() - 0.5) * 0.15;
        const pitchNoise = (Math.random() - 0.5) * 0.15;
        bot.look(
            bot.entity.yaw + yawNoise,
            bot.entity.pitch + pitchNoise,
            true,
        ).catch(() => {});
    }, 800);

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
                    dynamic: true,
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
        clearInterval(stuckMonitor);
        clearInterval(noiseMonitor);
        bot.clearControlStates();
    }
}
