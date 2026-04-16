// js/lib/tasks/utils.ts
import type { Bot } from "mineflayer";
import { type Block } from "prismarine-block";
import { type Item } from "prismarine-item";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";
import {
    TaskAbortError,
    NoTargetsNearbyError,
    isAbortError,
    ExecutionError,
    UnreachableError,
    BlockedMobError,
    StuckTerrainError,
    NoToolError,
} from "../errors.js";
import { steerTowards } from "../movement/navigator.js";
import { applyWorldMovementReflexes, senseWorld } from "../utils/world.js";

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

export function isOnSolidGround(bot: Bot): boolean {
    if (!bot.entity.onGround) return false;
    const pos = bot.entity.position.floored();
    const below = bot.blockAt(pos.offset(0, -1, 0));
    return !!below && !TREE_BLOCKS.has(below.name) && below.name !== "air";
}

export function isInFoliage(bot: Bot): boolean {
    const pos = bot.entity.position.floored();
    const at = bot.blockAt(pos);
    const below = bot.blockAt(pos.offset(0, -1, 0));
    return !!(
        (at && TREE_BLOCKS.has(at.name)) ||
        (below && TREE_BLOCKS.has(below.name))
    );
}

let lastAirborneAt = 0;
let wasAirborneRecently = false;

export async function escapeTree(bot: Bot, signal: AbortSignal): Promise<void> {
    // Optimization: only check if we might be in a tree (recently in air and falling/landed)
    const vel = bot.entity.velocity;
    const now = Date.now();

    if (!bot.entity.onGround) {
        lastAirborneAt = now;
        wasAirborneRecently = true;
    } else if (now - lastAirborneAt > 5000) {
        wasAirborneRecently = false;
    }

    // Skip if we are on ground and haven't been in the air for a while
    if (!wasAirborneRecently && bot.entity.onGround) return;

    if (!isInFoliage(bot)) return;
    try {
        bot.pathfinder.setGoal(null);
        bot.clearControlStates();
    } catch (_err) {}

    const MAX_ATTEMPTS = 30;
    for (let i = 0; i < MAX_ATTEMPTS; i++) {
        if (signal.aborted) throw new TaskAbortError();
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

                    await bot.dig(targetBlock, true, "auto");
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
            reject(new TaskAbortError());
            return;
        }
        const timer = setTimeout(() => {
            cleanup();
            resolve();
        }, ms);
        const onAbort = () => {
            clearTimeout(timer);
            cleanup();
            reject(new TaskAbortError());
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

import { Navigation } from "../movement/navigation.js";
import { RollingPathfinder } from "../movement/pathfinder.js";

export function moveToGoal(
    bot: Bot,
    goal: any,
    opts: MoveOptions,
): Promise<void> {
    const {
        signal,
        timeoutMs,
        stopMovement,
        stuckTimeoutMs = 2000,
    } = opts;

    return new Promise(async (resolve, reject) => {
        if (signal?.aborted) {
            return reject(new TaskAbortError());
        }

        const pathfinder = new RollingPathfinder(bot);
        const navigation = new Navigation(bot);

        let settled = false;
        let lastPos = bot.entity.position.clone();
        let stuckStrikes = 0;
        let ticksElapsed = 0;
        const timeoutTicks = Math.floor(timeoutMs / 50);

        const cleanup = () => {
            bot.removeListener("physicsTick", onTick);
            bot.clearControlStates();
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
                    : new TaskAbortError(),
            );

        if (signal) {
            signal.addEventListener("abort", onAbort, { once: true });
        }

        const onTick = async () => {
            if (settled) return;
            ticksElapsed++;

            if (ticksElapsed > timeoutTicks) {
                finish(new Error("timeout"));
                return;
            }

            // 1. Rolling Path Update
            await pathfinder.updatePath(goal);
            const waypoint = pathfinder.getNextWaypoint(3);

            if (!waypoint) {
                // If no waypoint and not at goal, we might be lost
                const distToGoal = bot.entity.position.distanceTo(goal);
                if (distToGoal > 2) {
                    stuckStrikes++;
                    if (stuckStrikes > 40) finish(new UnreachableError(`Cannot find path to goal (${distToGoal.toFixed(1)}m away)`));
                } else {
                    finish();
                }
                return;
            }

            // 2. Predictive Steering
            const controls = navigation.steer(waypoint);
            for (const [state, active] of Object.entries(controls)) {
                bot.setControlState(state as any, active);
            }

            // 3. Elite Stuck Detection (reason-based)
            if (ticksElapsed % 10 === 0) {
                const currentPos = bot.entity.position;
                const distMoved = lastPos.distanceTo(currentPos);
                const vel = bot.entity.velocity;
                const speed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);

                if (distMoved < 0.1 && speed < 0.05) {
                    stuckStrikes++;
                    if (stuckStrikes >= 3) {
                        // Classify the stuck reason
                        const nearbyMobs = Object.values(bot.entities).filter(e => 
                            e.type === 'mob' && e.position.distanceTo(currentPos) < 3
                        );
                        
                        if (nearbyMobs.length > 0) {
                            finish(new BlockedMobError(`Path blocked by ${nearbyMobs[0].name}`));
                        } else {
                            finish(new StuckTerrainError("Physically stuck in complex terrain"));
                        }
                    }
                } else {
                    stuckStrikes = Math.max(0, stuckStrikes - 1);
                }
                lastPos = currentPos.clone();
            }

            // 4. Goal Check
            if (bot.entity.position.distanceTo(goal) < 1.0) {
                finish();
            }
        };

        bot.on("physicsTick", onTick);
    });
}

export function findNearestBlockByName(
    bot: Bot,
    blockName: string,
): Block | null {
    return bot.findBlock({
        maxDistance: 32,
        matching: (block: Block) => block?.name === blockName,
    });
}

export async function makeRoomInInventory(
    bot: Bot,
    slotsNeeded: number = 1,
): Promise<void> {
    if (bot.inventory.emptySlotCount() >= slotsNeeded) return;

    const inventory = bot.inventory.items();
    const trashItems = inventory.filter((i) => TRASH_ITEMS.has(i.name));
    trashItems.sort((a, b) => a.count - b.count);

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
    bot: Bot,
    blockName: string,
): Promise<Block | null> {
    const item = bot.inventory.items().find((i) => i.name === blockName);
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

export async function collectBlocks(
    bot: Bot,
    candidateNames: string[],
    count: number,
    signal: AbortSignal,
): Promise<void> {
    const targets: Block[] = [];
    for (const name of candidateNames) {
        const blockId = (bot.registry as any).blocksByName[name]?.id;
        if (blockId === undefined) continue;

        const blockPositions = bot.findBlocks({
            matching: blockId,
            maxDistance: 64,
            count: count + 5,
        });

        const candidates = blockPositions
            .map((pos: Vec3) => bot.blockAt(pos))
            .filter((b): b is Block => b !== null);

        if (candidates.length > 0) {
            targets.push(...candidates);
        }

        if (targets.length >= count) break;
    }

    if (targets.length === 0) {
        throw new NoTargetsNearbyError(candidateNames[0]);
    }

    const onAbort = () => {
        if ((bot as any).collectBlock) (bot as any).collectBlock.cancelTask();
    };
    signal.addEventListener("abort", onAbort, { once: true });

    try {
        // Check for required tool before starting
        const firstTarget = targets[0];
        const harvestTool = bot.pathfinder.bestHarvestTool(firstTarget);
        const canHarvest = firstTarget.canHarvest(bot.inventory.items().find(i => i.type === harvestTool?.type)?.type ?? null);
        
        // If the block requires a tool to drop items but we don't have it, throw NO_TOOL
        // Note: logs can be harvested by hand, stone requires pickaxe.
        if (firstTarget.material?.includes('stone') && !canHarvest) {
            throw new NoToolError(`Missing tool to harvest ${firstTarget.name}`);
        }

        await (bot as any).collectBlock.collect(targets.slice(0, count));
    } catch (err: any) {
        if (isAbortError(err) || signal.aborted) {
            throw new TaskAbortError();
        }
        throw new Error(`COLLECT_FAILED: ${err.message}`);
    } finally {
        signal.removeEventListener("abort", onAbort);
    }
}
