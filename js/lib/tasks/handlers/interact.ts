import { type TaskContext } from "../registry.js";
import { findNearestEntity } from "../primitives.js";
import { findNearestBlockByName, moveToGoal } from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export async function handleInteract(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, stopMovement, timeouts } = ctx;
    const targetName = intent.target?.name;

    if (!targetName || targetName === "none") {
        throw new Error("missing interact target");
    }

    // 1. Try to find it as an Entity (Boats, Minecarts, Villagers)
    const entity = findNearestEntity(bot, targetName, 16);
    if (entity) {
        await moveToGoal(
            bot,
            new goals.GoalNear(
                entity.position.x,
                entity.position.y,
                entity.position.z,
                2,
            ),
            {
                signal,
                timeoutMs: timeouts.interact ?? 15000,
                stopMovement,
            },
        );
        if (
            targetName.includes("boat") ||
            targetName.includes("minecart") ||
            targetName.includes("horse")
        ) {
            await bot.mount(entity);
        } else {
            bot.activateEntity(entity);
        }
        return;
    }

    // 2. Try to find it as a Block (Doors, Buttons, Levers, Beds)
    const block = findNearestBlockByName(bot, targetName);
    if (block) {
        await moveToGoal(
            bot,
            new goals.GoalNear(
                block.position.x,
                block.position.y,
                block.position.z,
                2,
            ),
            {
                signal,
                timeoutMs: timeouts.interact ?? 15000,
                stopMovement,
            },
        );
        await bot.activateBlock(block);
        return;
    }

    throw new Error(`NO_INTERACTABLE_FOUND: ${targetName}`);
}
