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

async function ensureKinematicStop(bot: Bot, timeoutMs: number = 1500): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        if (!bot.entity) return;
        const vel = (bot.entity as any).velocity;
        if (!vel) return;
        const horizontalVel = Math.sqrt(vel.x * vel.x + vel.z * vel.z);
        if (horizontalVel < 0.01) return;
        await new Promise((resolve) => setTimeout(resolve, 50));
    }
}

export async function navigateWithFallbacks(
    bot: Bot,
    goal: any,
    opts: NavOpts,
): Promise<void> {
    bot.clearControlStates();
    if (!bot.entity || !bot.entity.position) {
        throw new ExecutionError("Bot not spawned", "BLOCKED", 0);
    }
    const maxRetries = opts.maxRetries ?? 3;
    let attempts = 0;
    const targetVec = new Vec3(goal.x ?? bot.entity.position.x, goal.y ?? bot.entity.position.y, goal.z ?? bot.entity.position.z);
    const tracker = new ProgressTracker(bot, targetVec);
    let strategyController: AbortController | null = null;
    let stuckPoll: NodeJS.Timeout | null = null;

    try {
        stuckPoll = setInterval(() => {
            if (tracker.checkStuck(bot)) {
                log.warn("Proactive stuck detection triggered in navigator.");
                strategyController?.abort(new Error("stuck"));
            }
        }, 1000);

        while (attempts < maxRetries) {
            bot.clearControlStates();
            if (opts.signal?.aborted) {
                bot.clearControlStates();
                throw new ExecutionError("aborted", "aborted", tracker.getProgress(bot));
            }
            strategyController = new AbortController();
            const strategySignal = strategyController.signal;
            const onParentAbort = () => {
                bot.clearControlStates();
                strategyController?.abort();
            };
            opts.signal?.addEventListener("abort", onParentAbort);

            try {
                if (attempts === 0) {
                    await new Promise((resolve) => setTimeout(resolve, 150 + Math.random() * 300));
                }
                if (attempts === 0) {
                    await moveToGoal(bot, goal, {
                        timeoutMs: opts.timeoutMs ?? 15000,
                        dynamic: false,
                        signal: strategySignal,
                        stopMovement: opts.stopMovement,
                    });
                    await ensureKinematicStop(bot);
                    return;
                }
                if (attempts === 1) {
                    log.info("Strategy 2: Straight-line movement fallback");
                    await straightLineMove(bot, { x: targetVec.x, z: targetVec.z }, 3000, strategySignal);
                }
                if (attempts === 2) {
                    log.info("Strategy 3: Random walk fallback");
                    await randomWalk(bot, 3000, strategySignal);
                }
                const dist = tracker.getDistance(bot);
                if (dist < 2) {
                    await ensureKinematicStop(bot);
                    return;
                }
                throw new Error("strategy_exhausted");
            } catch (err: any) {
                if (opts.signal?.aborted) {
                    bot.clearControlStates();
                    throw new ExecutionError("aborted", "aborted", tracker.getProgress(bot));
                }
                attempts++;
                log.warn(`Movement strategy failed (attempt ${attempts}/${maxRetries}).`, { error: err.message });
                if (attempts >= maxRetries) {
                    bot.clearControlStates();
                    throw new ExecutionError("pathing_failed", "blocked", tracker.getProgress(bot));
                }
            } finally {
                opts.signal?.removeEventListener("abort", onParentAbort);
                strategyController = null;
            }
        }
    } finally {
        if (stuckPoll) clearInterval(stuckPoll);
        bot.clearControlStates();
        await ensureKinematicStop(bot);
    }
}
