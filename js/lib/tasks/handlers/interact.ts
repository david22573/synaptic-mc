import { type TaskContext } from "../registry.js";
import { findNearestEntity } from "../primitives.js";
import { findNearestBlockByName } from "../utils.js";
import { navigateWithFallbacks } from "../../movement/navigator.js";
import { Runtime } from "../../control/runtime.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export async function handleInteract(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, stopMovement, timeouts } = ctx;

    const taskLogic = async () => {
        const targetName = intent.target?.name;

        if (!targetName || targetName === "none") {
            throw new Error("missing interact target");
        }

        const entity = findNearestEntity(bot, targetName, 16);
        if (entity) {
            await navigateWithFallbacks(
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
                    maxRetries: 2,
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

        const block = findNearestBlockByName(bot, targetName);
        if (block) {
            await navigateWithFallbacks(
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
                    maxRetries: 2,
                },
            );
            await bot.activateBlock(block);
            return;
        }

        throw new Error(`NO_INTERACTABLE_FOUND: ${targetName}`);
    };

    await new Runtime(bot).execute(taskLogic(), signal);
}
