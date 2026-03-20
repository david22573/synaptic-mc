import { type Bot } from "mineflayer";
import { Entity } from "prismarine-entity";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export async function gotoEntity(
    bot: Bot,
    entity: Entity,
    range: number,
): Promise<boolean> {
    const goal = new goals.GoalFollow(entity, range);
    try {
        // dynamic=true is critical here for chasing moving mobs
        await (bot as any).pathfinder.goto(goal, true);
        return true;
    } catch (err) {
        // Suppress pathing errors since mobs moving out of range naturally causes them
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
    return bot.nearestEntity((entity) => {
        return (
            entity.name === name &&
            bot.entity.position.distanceTo(entity.position) <= radius
        );
    });
}
