import type { Bot } from "mineflayer";
import type { Entity } from "prismarine-entity";
import pkg from "mineflayer-pathfinder";
import { moveToGoal, type MoveOptions } from "./utils.js";

const { goals } = pkg;

export async function gotoEntity(
    bot: Bot,
    entity: Entity,
    range: number,
    opts: MoveOptions,
): Promise<boolean> {
    const goal = new goals.GoalFollow(entity, range);

    try {
        await moveToGoal(bot, goal, {
            ...opts,
            dynamic: true,
            stuckTimeoutMs: 3000, // Chasing shouldn't stall for long
        });

        return true;
    } catch (err: any) {
        if (err.message === "aborted") {
            throw err;
            // Bubble up explicit aborts immediately
        }

        // Suppress general pathing/stuck errors for entity chasing
        bot.pathfinder.setGoal(null);
        bot.clearControlStates();
        return false;
    }
}

export async function attackEntity(bot: Bot, entity: Entity): Promise<void> {
    await bot.lookAt(entity.position.offset(0, entity.height ?? 1.5, 0), true);
    bot.attack(entity);
}

export function findNearestEntity(
    bot: Bot,
    name: string,
    radius: number,
): Entity | null {
    const isGenericAnimal =
        name === "animal" || name === "passive_animals" || name === "food";
    const isGenericMonster =
        name === "monster" || name === "threat" || name === "hostile";

    return bot.nearestEntity((entity) => {
        if (bot.entity.position.distanceTo(entity.position) > radius)
            return false;

        if (isGenericAnimal) {
            return ["pig", "cow", "sheep", "chicken", "rabbit"].includes(
                entity.name!,
            );
        }
        if (isGenericMonster) {
            return entity.type === "hostile" || entity.type === "mob";
        }

        return entity.name === name;
    });
}
