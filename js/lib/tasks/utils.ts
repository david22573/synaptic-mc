// js/lib/tasks/utils.ts
import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";
import { ExecutionError } from "./primitives.js";
import { steerTowards } from "../movement/navigator.js";

const { goals } = pkg;

export const LOG_BLOCK_NAMES = [
    "oak_log",
    "birch_log",
    "spruce_log",
    "jungle_log",
    "acacia_log",
    "dark_oak_log",
    "mangrove_log",
    "cherry_log",
] as const;

const TREE_BLOCKS = new Set([
    "oak_leaves",
    "birch_leaves",
    "spruce_leaves",
    "jungle_leaves",
    "acacia_leaves",
    "dark_oak_leaves",
    "mangrove_leaves",
    "azalea_leaves",
    "flowering_azalea_leaves",
    "cherry_leaves",
    ...LOG_BLOCK_NAMES,
]);

const TRASH_ITEMS = new Set([
    "dirt",
    "cobblestone",
    "gravel",
    "rotten_flesh",
    "spider_eye",
    "bone",
    "string",
    "andesite",
    "diorite",
    "granite",
    "seeds",
    "tall_grass",
    "grass",
]);

export function isOnSolidGround(bot: any): boolean {
    if (!bot.entity.onGround) return false;
    const pos = bot.entity.position.floored();
    const below = bot.blockAt(pos.offset(0, -1, 0));
    return !!below && !TREE_BLOCKS.has(below.name) && below.name !== "air";
}

export function isInFoliage(bot: any): boolean {
    const pos = bot.entity.position.floored();
    const at = bot.blockAt(pos);
    const below = bot.blockAt(pos.offset(0, -1, 0));
    return (
        (at && TREE_BLOCKS.has(at.name)) ||
        (below && TREE_BLOCKS.has(below.name))
    );
}

export async function escapeTree(bot: any, signal: AbortSignal): Promise<void> {
    if (!isInFoliage(bot)) return;
    try {
        bot.pathfinder.setGoal(null);
        bot.clearControlStates();
    } catch (_err) {}

    const MAX_ATTEMPTS = 30;
    for (let i = 0; i < MAX_ATTEMPTS; i++) {
        if (signal.aborted) throw new Error("aborted");
        if (isOnSolidGround(bot)) return;

        const pos = bot.entity.position.floored();
        const blocksToClear = [
            bot.blockAt(pos.offset(0, 1, 0)),
            bot.blockAt(pos),
            bot.blockAt(pos.offset(0, -1, 0)),
            bot.blockAt(pos.offset(1, 0, 0)),
            bot.blockAt(pos.offset(-1, 0, 0)),
            bot.blockAt(pos.offset(0, 0, 1)),
            bot.blockAt(pos.offset(0, 0, -1)),
        ];

        let clearedSomething = false;
        for (const targetBlock of blocksToClear) {
            if (
                targetBlock &&
                TREE_BLOCKS.has(targetBlock.name) &&
                targetBlock.name !== "air"
            ) {
                try {
                    const tool = bot.pathfinder.bestHarvestTool(targetBlock);
                    if (tool) await bot.equip(tool, "hand");
                    else await bot.unequip("hand");

                    await bot.dig(targetBlock, true, "ignore");
                    clearedSomething = true;
                } catch (err) {
                    // Ignore dig errors
                }
            }
        }

        if (!clearedSomething && !isOnSolidGround(bot)) {
            const dir = Math.random() > 0.5 ? "forward" : "back";
            bot.setControlState(dir, true);
            await bot.waitForTicks(5); // ~250ms synced
            bot.setControlState(dir, false);
        }

        await bot.waitForTicks(2); // ~100ms synced
    }

    if (!isOnSolidGround(bot)) {
        throw new Error(
            "EscapeTree - could not reach ground after 30 attempts",
        );
    }
}

// Keeping as wall-clock time since it doesn't take the bot instance
// and might be used for general non-physics async operations.
export function waitForMs(ms: number, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
        if (signal.aborted) {
            reject(new Error("aborted"));
            return;
        }
        const timer = setTimeout(() => {
            cleanup();
            resolve();
        }, ms);
        const onAbort = () => {
            clearTimeout(timer);
            cleanup();
            reject(new Error("aborted"));
        };
        const cleanup = () => {
            signal.removeEventListener("abort", onAbort);
        };
        signal.addEventListener("abort", onAbort, { once: true });
    });
}

export interface MoveOptions {
    signal?: AbortSignal;
    timeoutMs: number;
    stopMovement?: () => void;
    dynamic?: boolean;
    stuckTimeoutMs?: number;
}

export function moveToGoal(
    bot: any,
    goal: any,
    opts: MoveOptions,
): Promise<void> {
    const {
        signal,
        timeoutMs,
        stopMovement,
        dynamic = false,
        stuckTimeoutMs = 2000,
    } = opts;

    return new Promise((resolve, reject) => {
        if (signal?.aborted) {
            return reject(new Error("aborted"));
        }

        if (goal.isEnd && goal.isEnd(bot.entity.position)) {
            return resolve();
        }

        let settled = false;
        let lastPos = bot.entity.position.clone();
        let stuckStrikes = 0;
        let ticksElapsed = 0;
        const timeoutTicks = Math.floor(timeoutMs / 50);

        const listeners = new Map<string, any>();

        const cleanup = () => {
            bot.removeListener("physicsTick", onTick);
            listeners.forEach((handler, event) =>
                bot.removeListener(event, handler),
            );
            listeners.clear();
        };

        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;
            cleanup();
            if (err) {
                if (stopMovement) {
                    try {
                        stopMovement();
                    } catch (e) {}
                }
                reject(err);
                return;
            }
            resolve();
        };

        const onAbort = () =>
            finish(
                signal?.reason instanceof Error
                    ? signal.reason
                    : new Error("aborted"),
            );

        const onGoalReached = () => {
            finish();
        };

        const onPathUpdate = (results: any) => {
            if (results.status === "noPath") {
                finish(new Error("no_path"));
            } else if (results.status === "timeout") {
                finish(new Error("pathfinder_timeout"));
            }
        };

        const onTick = () => {
            if (settled) return;
            ticksElapsed++;

            if (ticksElapsed > timeoutTicks) {
                finish(new Error("timeout"));
                return;
            }

            // Engine-synced stuck checking (~800ms intervals)
            if (ticksElapsed % 16 === 0) {
                const currentPos = bot.entity.position;
                const dist = lastPos.distanceTo(currentPos);
                const vel = bot.entity.velocity;
                const speed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);

                if (bot.pathfinder.isMoving() && dist < 0.05 && speed < 0.02) {
                    stuckStrikes++;
                    if (stuckStrikes >= 2) {
                        finish(
                            new ExecutionError(
                                "bot physically stuck during pathing",
                                "STUCK",
                                0.0,
                            ),
                        );
                    }
                } else {
                    stuckStrikes = 0;
                }
                lastPos = currentPos.clone();
            }

            // Hijack the final approach with per-tick continuous steering
            if (
                bot.pathfinder.isMoving() &&
                goal.x !== undefined &&
                goal.z !== undefined
            ) {
                const currentPos = bot.entity.position;
                const targetVec = new Vec3(
                    goal.x,
                    goal.y ?? currentPos.y,
                    goal.z,
                );
                const distToGoal = currentPos.distanceTo(targetVec);

                if (distToGoal < 2.0) {
                    steerTowards(bot, targetVec);
                }
            }
        };

        listeners.set("goal_reached", onGoalReached);
        listeners.set("path_update", onPathUpdate);

        if (signal) {
            signal.addEventListener("abort", onAbort, { once: true });
        }

        bot.on("goal_reached", onGoalReached);
        bot.on("path_update", onPathUpdate);
        bot.on("physicsTick", onTick);

        try {
            bot.pathfinder.setGoal(goal, dynamic);
        } catch (err) {
            finish(err instanceof Error ? err : new Error(String(err)));
        }
    });
}

export function findNearestBlockByName(bot: Bot, blockName: string): any {
    return bot.findBlock({
        maxDistance: 32,
        matching: (block: any) => block?.name === blockName,
    });
}

export async function makeRoomInInventory(
    bot: any,
    slotsNeeded: number = 1,
): Promise<void> {
    if (bot.inventory.emptySlotCount() >= slotsNeeded) return;

    const inventory = bot.inventory.items();
    const trashItems = inventory.filter((i: any) => TRASH_ITEMS.has(i.name));
    trashItems.sort((a: any, b: any) => a.count - b.count);

    let slotsFreed = 0;
    for (const item of trashItems) {
        if (slotsFreed >= slotsNeeded) break;
        try {
            const yaw = bot.entity.yaw;
            await bot.look(yaw, -0.3, true);
            await bot.tossStack(item);
            await bot.waitForTicks(6); // ~300ms synced
            slotsFreed++;
        } catch (err) {
            // Ignore failure, try next item
        }
    }
}

export async function placePortableUtility(
    bot: any,
    blockName: string,
): Promise<any> {
    const item = bot.inventory.items().find((i: any) => i.name === blockName);
    if (!item) return null;

    const pos = bot.entity.position.floored();

    const offsets = [
        new Vec3(1, 0, 0),
        new Vec3(-1, 0, 0),
        new Vec3(0, 0, 1),
        new Vec3(0, 0, -1),
        new Vec3(1, 0, 1),
        new Vec3(-1, 0, -1),
    ];

    for (const offset of offsets) {
        const placePos = pos.plus(offset);
        const blockAt = bot.blockAt(placePos);
        const belowBlock = bot.blockAt(placePos.offset(0, -1, 0));

        if (
            blockAt &&
            blockAt.name === "air" &&
            belowBlock &&
            belowBlock.boundingBox === "block"
        ) {
            try {
                await bot.equip(item.type, "hand");
                const faceVector = new Vec3(0, 1, 0);
                await bot.placeBlock(belowBlock, faceVector);
                await bot.waitForTicks(5); // ~250ms synced
                return bot.blockAt(placePos);
            } catch (err) {
                // Try next position
            }
        }
    }
    return null;
}
