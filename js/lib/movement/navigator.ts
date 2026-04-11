// js/lib/movement/navigator.ts
import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import { ProgressTracker } from "./progress.js";
import { straightLineMove, randomWalk } from "./recovery.js";
import { moveToGoal } from "../tasks/utils.js";
import { log } from "../logger.js";
import { ExecutionError } from "../tasks/primitives.js";
import { applyHesitation } from "./hesitation.js";

export interface NavOpts {
    timeoutMs?: number;
    signal?: AbortSignal;
    stopMovement?: () => void;
    maxRetries?: number;
}

async function ensureKinematicStop(
    bot: Bot,
    timeoutMs: number = 1500,
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
    const maxRetries = opts.maxRetries ?? 3;
    let attempts = 0;
    const targetVec = new Vec3(
        goal.x ?? bot.entity.position.x,
        goal.y ?? bot.entity.position.y,
        goal.z ?? bot.entity.position.z,
    );
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
                if (attempts === 0) {
                    await applyHesitation(bot);
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
                    await straightLineMove(
                        bot,
                        { x: targetVec.x, z: targetVec.z },
                        3000,
                        strategySignal,
                    );
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
        await ensureKinematicStop(bot);
    }
}

// Core continuous steering primitive. Call this inside intent evaluators or physicsTicks.
export function steerTowards(
    bot: Bot,
    target: Vec3,
    stopDistance: number = 1.0,
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

    return controls;
}
