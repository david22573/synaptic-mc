import pkg from "mineflayer-pathfinder";
import { moveToGoal } from "./utils.js";
const { goals } = pkg;
export async function gotoEntity(bot, entity, range, opts) {
    const goal = new goals.GoalFollow(entity, range);
    try {
        await moveToGoal(bot, goal, {
            ...opts,
            dynamic: true,
            stuckTimeoutMs: 3000, // Chasing shouldn't stall for long
        });
        return true;
    }
    catch (err) {
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
export async function attackEntity(bot, entity) {
    await bot.lookAt(entity.position.offset(0, entity.height ?? 1.5, 0), true);
    bot.attack(entity);
}
export function findNearestEntity(bot, name, radius) {
    const isGenericAnimal = name === "animal" || name === "passive_animals" || name === "food";
    const isGenericMonster = name === "monster" || name === "threat" || name === "hostile";
    return bot.nearestEntity((entity) => {
        if (bot.entity.position.distanceTo(entity.position) > radius)
            return false;
        if (isGenericAnimal) {
            return ["pig", "cow", "sheep", "chicken", "rabbit"].includes(entity.name);
        }
        if (isGenericMonster) {
            return entity.type === "hostile" || entity.type === "mob";
        }
        return entity.name === name;
    });
}
//# sourceMappingURL=primitives.js.map