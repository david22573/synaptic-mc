// js/lib/movement/navigator.ts
import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import { ProgressTracker } from "./progress.js";
import {
    straightLineMove,
    randomWalk,
    sideStepRecovery,
    backOffRecovery,
} from "./recovery.js";
import { moveToGoal } from "../tasks/utils.js";
import { log } from "../logger.js";
import { ExecutionError } from "../errors.js";
import { applyHesitation } from "./hesitation.js";

export interface NavOpts {
    timeoutMs?: number;
    signal?: AbortSignal;
    stopMovement?: () => void;
    maxRetries?: number;
    skipStop?: boolean; // New option to skip stop for fluidity
}

async function ensureKinematicStop(
    bot: Bot,
    timeoutMs: number = 400, // Reduced from 1500 to 400ms
): Promise<void> {
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
    const maxRetries = opts.maxRetries ?? 5; // Increased default retries
    let attempts = 0;
    const getGoalPos = (): Vec3 | null => {
        if (goal.x !== undefined && goal.z !== undefined) {
            return new Vec3(goal.x, goal.y ?? bot.entity.position.y, goal.z);
        }
        if ((goal as any).dest) {
            const dest = (goal as any).dest;
            return new Vec3(dest.x, dest.y, dest.z);
        }
        if ((goal as any).entity?.position) {
            return (goal as any).entity.position;
        }
        return null;
    };

    const targetVec = getGoalPos() || bot.entity.position.clone();
    const tracker = new ProgressTracker(bot, targetVec);
    let strategyController: AbortController | null = null;

    try {
        while (attempts < maxRetries) {
            bot.clearControlStates();
            if (opts.signal?.aborted) {
                bot.clearControlStates();
                throw new ExecutionError(
                    "aborted",
                    "aborted",
                    tracker.getProgress(bot),
                );
            }
            strategyController = new AbortController();
            const strategySignal = strategyController.signal;
            const onParentAbort = () => {
                bot.clearControlStates();
                strategyController?.abort();
            };
            opts.signal?.addEventListener("abort", onParentAbort);

            try {
                // Strategy Selection
                if (attempts === 0) {
                    await moveToGoal(bot, goal, {
                        timeoutMs: opts.timeoutMs ?? 20000,
                        dynamic: false,
                        signal: strategySignal,
                        stopMovement: opts.stopMovement,
                    });
                    if (!opts.skipStop) await ensureKinematicStop(bot);
                    return;
                }
                
                if (attempts === 1) {
                    log.info("Strategy: Side-step recovery");
                    await sideStepRecovery(bot, 1200, strategySignal);
                } else if (attempts === 2) {
                    log.info("Strategy: Back-off recovery");
                    await backOffRecovery(bot, 1000, strategySignal);
                } else if (attempts === 3) {
                    log.info("Strategy: Straight-line fallback");
                    await straightLineMove(
                        bot,
                        { x: targetVec.x, z: targetVec.z },
                        3500,
                        strategySignal,
                    );
                } else if (attempts === 4) {
                    log.info("Strategy: Random walk fallback");
                    await randomWalk(bot, 4000, strategySignal);
                }

                // After a recovery strategy, try pathfinding again if not yet at goal
                const dist = tracker.getDistance(bot);
                if (dist < 2) {
                    if (!opts.skipStop) await ensureKinematicStop(bot);
                    return;
                }
                
                // If we haven't returned, the loop continues and tries moveToGoal (attempt 0 equivalent logic)
                // We'll reset attempt 0 behavior by letting the loop continue and next attempt will try pathfinding again
                // Actually, let's explicitly try pathfinding after recovery.
                log.info("Recovery complete, retrying pathfinder...");
                await moveToGoal(bot, goal, {
                    timeoutMs: 12000,
                    dynamic: false,
                    signal: strategySignal,
                    stopMovement: opts.stopMovement,
                });
                return;
            } catch (err: any) {
                if (opts.signal?.aborted) {
                    bot.clearControlStates();
                    throw new ExecutionError(
                        "aborted",
                        "aborted",
                        tracker.getProgress(bot),
                    );
                }
                attempts++;
                log.warn(
                    `Movement strategy failed (attempt ${attempts}/${maxRetries}).`,
                    { error: err.message },
                );
                if (attempts >= maxRetries) {
                    bot.clearControlStates();
                    throw new ExecutionError(
                        "pathing_failed",
                        "blocked",
                        tracker.getProgress(bot),
                    );
                }
            } finally {
                opts.signal?.removeEventListener("abort", onParentAbort);
                strategyController = null;
            }
        }
    } finally {
        bot.clearControlStates();
        if (!opts.skipStop) await ensureKinematicStop(bot);
    }
}

// Core continuous steering primitive. Call this inside intent evaluators or physicsTicks.
export function steerTowards(
    bot: Bot,
    target: Vec3,
    stopDistance: number = 1.0,
    applyControls: boolean = false,
): Record<string, boolean> {
    const controls = {
        forward: false,
        back: false,
        left: false,
        right: false,
        jump: false,
        sprint: false,
    };
    if (!bot.entity) return controls;

    const pos = bot.entity.position;
    const dist = pos.distanceTo(target);

    if (dist <= stopDistance) {
        if (applyControls) bot.clearControlStates();
        return controls; // Goal reached, kill throttle
    }

    controls.forward = true;
    controls.sprint = true;

    // Per-tick Aim and Velocity Correction
    const dx = target.x - pos.x;
    const dz = target.z - pos.z;
    const dy = target.y - pos.y;

    let desiredYaw = Math.atan2(-dx, -dz);
    const dist2d = Math.sqrt(dx * dx + dz * dz);
    const desiredPitch = Math.atan2(dy, dist2d);

    const vel = bot.entity.velocity;
    const horizontalSpeed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);

    // If we're moving fast enough, apply a counter-steer to correct drift
    if (horizontalSpeed > 0.05) {
        const currentTravelYaw = Math.atan2(-vel.x, -vel.z);
        const driftAngle = desiredYaw - currentTravelYaw;
        // Oversteer slightly into the drift to push velocity vector towards target
        desiredYaw += driftAngle * 0.4;
    }

    // Lock aim per-tick
    bot.look(desiredYaw, desiredPitch, true);

    // Continuous auto-jump (primitive parkour logic)
    const yaw = bot.entity.yaw;
    const blockInFront = bot.blockAt(
        pos.offset(-Math.sin(yaw), 0, -Math.cos(yaw)),
    );
    const blockAbove = bot.blockAt(
        pos.offset(-Math.sin(yaw), 1, -Math.cos(yaw)),
    );

    if (
        blockInFront &&
        blockInFront.boundingBox === "block" &&
        blockAbove &&
        blockAbove.name === "air"
    ) {
        controls.jump = true;
    }

    if (applyControls) {
        Object.entries(controls).forEach(([state, active]) => {
            bot.setControlState(state as any, active);
        });
    }

    return controls;
}
