// js/lib/tasks/primitives.ts
import type { Bot } from "mineflayer";
import type { Entity } from "prismarine-entity";
import pkg from "mineflayer-pathfinder";
import { type MoveOptions } from "./utils.js";
import { navigateWithFallbacks } from "../movement/navigator.js";

const { goals } = pkg;

export class ExecutionError extends Error {
    constructor(
        message: string,
        public cause: string,
        public progress: number,
    ) {
        super(message);
        this.name = "ExecutionError";
    }
}

export async function gotoEntity(
    bot: Bot,
    entity: Entity,
    range: number,
    opts: MoveOptions,
): Promise<boolean> {
    const goal = new goals.GoalFollow(entity, range);

    // Phase 1: Track distance to calculate partial success
    const startPos = bot.entity.position.clone();
    const startDist = startPos.distanceTo(entity.position);

    try {
        await navigateWithFallbacks(bot, goal, {
            timeoutMs: opts.timeoutMs ?? 15000,
            signal: opts.signal,
            stopMovement: opts.stopMovement,
            maxRetries: 3,
        });
        return true;
    } catch (err: any) {
        // Phase 1/5: Catch failure, measure progress, and throw structured ExecutionError
        const currentDist = bot.entity.position.distanceTo(entity.position);
        const progress =
            startDist > 0 ? Math.max(0, 1 - currentDist / startDist) : 0;

        throw new ExecutionError(
            err.message || "Failed to reach entity",
            err.cause || "BLOCKED",
            progress,
        );
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
