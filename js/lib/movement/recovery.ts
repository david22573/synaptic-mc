// js/lib/movement/recovery.ts
import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";

export async function straightLineMove(
    bot: Bot,
    target: { x: number; z: number },
    durationMs: number = 2000,
    signal?: AbortSignal,
): Promise<void> {
    bot.clearControlStates();
    if (!bot.entity) return;
    if (signal?.aborted) return;

    bot.setControlState("forward", true);
    bot.setControlState("sprint", true);

    const targetVec = new Vec3(target.x, bot.entity.position.y, target.z);

    // Bind to the engine's physics loop for reactive per-tick updates
    const onTick = () => {
        if (!bot.entity) return;
        bot.lookAt(new Vec3(target.x, bot.entity.position.y, target.z), true);

        if ((bot.entity as any).isCollidedHorizontally) {
            bot.setControlState("jump", true);
        } else {
            bot.setControlState("jump", false);
        }
    };

    bot.on("physicsTick", onTick);

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
            bot.removeListener("physicsTick", onTick);
            clearTimeout(timeout);
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
    bot.clearControlStates();
    if (!bot.entity) return;
    if (signal?.aborted) return;

    const rx = bot.entity.position.x + (Math.random() * 16 - 8);
    const rz = bot.entity.position.z + (Math.random() * 16 - 8);
    const targetVec = new Vec3(rx, bot.entity.position.y, rz);

    bot.setControlState("forward", true);

    const onTick = () => {
        if (!bot.entity) return;
        bot.lookAt(targetVec, true);

        if (
            Math.random() > 0.95 ||
            (bot.entity as any).isCollidedHorizontally
        ) {
            bot.setControlState("jump", true);
        } else {
            bot.setControlState("jump", false);
        }
    };

    bot.on("physicsTick", onTick);

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
            bot.removeListener("physicsTick", onTick);
            clearTimeout(timeout);
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
    bot.clearControlStates();
    if (!bot.entity) return;

    bot.setControlState("jump", true);
    bot.setControlState("left", Math.random() > 0.5);
    bot.setControlState("right", Math.random() > 0.5);
    bot.setControlState("back", Math.random() > 0.5);
    bot.setControlState("forward", Math.random() > 0.5);

    return new Promise((resolve) => {
        setTimeout(() => {
            bot.clearControlStates();
            resolve();
        }, durationMs);
    });
}
