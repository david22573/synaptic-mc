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

export async function sideStepRecovery(
    bot: Bot,
    durationMs: number = 1000,
    signal?: AbortSignal,
): Promise<void> {
    bot.clearControlStates();
    if (!bot.entity) return;
    if (signal?.aborted) return;

    const left = Math.random() > 0.5;
    bot.setControlState(left ? "left" : "right", true);
    bot.setControlState("forward", true);
    bot.setControlState("jump", true);
    bot.setControlState("sprint", true);

    return new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
            bot.clearControlStates();
            resolve();
        }, durationMs);

        const onAbort = () => {
            bot.clearControlStates();
            clearTimeout(timeout);
            reject(new Error("aborted"));
        };

        if (signal) {
            signal.addEventListener("abort", onAbort, { once: true });
        }
    });
}

export async function backOffRecovery(
    bot: Bot,
    durationMs: number = 800,
    signal?: AbortSignal,
): Promise<void> {
    bot.clearControlStates();
    if (!bot.entity) return;
    if (signal?.aborted) return;

    bot.setControlState("back", true);
    bot.setControlState("sprint", false);
    if (Math.random() > 0.5) {
        bot.setControlState(Math.random() > 0.5 ? "left" : "right", true);
    }

    return new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
            bot.clearControlStates();
            resolve();
        }, durationMs);

        const onAbort = () => {
            bot.clearControlStates();
            clearTimeout(timeout);
            reject(new Error("aborted"));
        };

        if (signal) {
            signal.addEventListener("abort", onAbort, { once: true });
        }
    });
}

export async function emergencyFlee(
    bot: Bot,
    durationMs: number = 3000,
): Promise<void> {
    bot.clearControlStates();
    if (!bot.entity) return;

    // Turn 180 degrees from current look
    bot.look(bot.entity.yaw + Math.PI, bot.entity.pitch, true);
    bot.setControlState("forward", true);
    bot.setControlState("sprint", true);
    bot.setControlState("jump", true);

    return new Promise((resolve) => {
        setTimeout(() => {
            bot.clearControlStates();
            resolve();
        }, durationMs);
    });
}

export async function autoEat(bot: Bot): Promise<void> {
    const food = bot.inventory
        .items()
        .find((item) => item.name.includes("cooked") || item.name === "apple");
    if (food) {
        try {
            await bot.equip(food, "hand");
            await bot.consume();
        } catch (err) {
            // ignore eat errors
        }
    }
}

export function preventFall(bot: Bot) {
    if (!bot.entity) return;
    // Simple reflex: if vertical velocity is negative and large, try to sneak or water bucket (if we had one)
    if (bot.entity.velocity.y < -0.6) {
        bot.setControlState("sneak", true);
    } else {
        bot.setControlState("sneak", false);
    }
}

export async function unstuckLogic(bot: Bot): Promise<void> {
    bot.clearControlStates();
    bot.setControlState("jump", true);
    bot.setControlState("back", true);
    await new Promise((r) => setTimeout(r, 500));
    bot.setControlState("back", false);
    bot.setControlState("left", Math.random() > 0.5);
    await new Promise((r) => setTimeout(r, 500));
    bot.clearControlStates();
}

export async function lagRecovery(bot: Bot): Promise<void> {
    bot.clearControlStates();
    bot.pathfinder.setGoal(null);
    await bot.waitForTicks(10); // Wait 500ms for server catch-up
}

export async function fallRecovery(bot: Bot): Promise<void> {
    if (!bot.entity) return;
    bot.setControlState("sneak", true);
    await bot.waitForTicks(20);
    bot.setControlState("sneak", false);
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
