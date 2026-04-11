// js/lib/movement/recovery.ts
import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";

export async function straightLineMove(
    bot: Bot,
    target: { x: number; z: number },
    durationMs: number = 2000,
    signal?: AbortSignal,
): Promise<void> {
    // Unconditional sanitization before starting movement
    bot.clearControlStates();
    if (!bot.entity) return;
    if (signal?.aborted) return;

    bot.lookAt(new Vec3(target.x, (bot.entity as any).position.y, target.z), true);
    bot.setControlState("forward", true);
    bot.setControlState("sprint", true);

    const jumpInterval = setInterval(() => {
        if ((bot.entity as any).isCollidedHorizontally) {
            bot.setControlState("jump", true);
        } else {
            bot.setControlState("jump", false);
        }
    }, 100);

    return new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
            cleanup();
            resolve();
        }, durationMs);

        const onAbort = () => {
            cleanup();
            reject(new Error("aborted"));
        };

        const cleanup = () => {
            clearInterval(jumpInterval);
            clearTimeout(timeout);
            // Phase 7: Enforce global state sanitization unconditionally on task abort or completion
            bot.clearControlStates();
            signal?.removeEventListener("abort", onAbort);
        };

        if (signal) {
            signal.addEventListener("abort", onAbort, { once: true });
        }
    });
}

export async function randomWalk(
    bot: Bot,
    durationMs: number = 2000,
    signal?: AbortSignal,
): Promise<void> {
    // Unconditional sanitization before starting movement
    bot.clearControlStates();
    if (!bot.entity) return;
    if (signal?.aborted) return;

    const rx = (bot.entity as any).position.x + (Math.random() * 16 - 8);
    const rz = (bot.entity as any).position.z + (Math.random() * 16 - 8);

    bot.lookAt(new Vec3(rx, (bot.entity as any).position.y, rz), true);
    bot.setControlState("forward", true);

    const jumpInterval = setInterval(() => {
        if (Math.random() > 0.8 || (bot.entity as any).isCollidedHorizontally) {
            bot.setControlState("jump", true);
        } else {
            bot.setControlState("jump", false);
        }
    }, 200);

    return new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
            cleanup();
            resolve();
        }, durationMs);

        const onAbort = () => {
            cleanup();
            reject(new Error("aborted"));
        };

        const cleanup = () => {
            clearInterval(jumpInterval);
            clearTimeout(timeout);
            // Phase 7: Enforce global state sanitization unconditionally on task abort or completion
            bot.clearControlStates();
            signal?.removeEventListener("abort", onAbort);
        };

        if (signal) {
            signal.addEventListener("abort", onAbort, { once: true });
        }
    });
}

export async function jumpRecovery(
    bot: Bot,
    durationMs: number = 1000,
): Promise<void> {
    // Unconditional sanitization before starting movement
    bot.clearControlStates();
    if (!bot.entity) return;
    bot.setControlState("jump", true);
    bot.setControlState("left", Math.random() > 0.5);
    bot.setControlState("right", Math.random() > 0.5);
    bot.setControlState("back", Math.random() > 0.5);
    bot.setControlState("forward", Math.random() > 0.5);

    return new Promise((resolve) => {
        setTimeout(() => {
            // Phase 7: Enforce global state sanitization unconditionally on completion
            bot.clearControlStates();
            resolve();
        }, durationMs);
    });
}
