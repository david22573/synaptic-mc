import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";

const { goals } = pkg;

// ==========================================
// CONSTANTS
// ==========================================

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
    "oak_log",
    "birch_log",
    "spruce_log",
    "jungle_log",
    "acacia_log",
    "dark_oak_log",
    "mangrove_log",
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

// ==========================================
// MOVEMENT & POSITION HELPERS
// ==========================================

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

    // Increased from 16 to 30 to account for massive jungle/custom canopies
    const MAX_ATTEMPTS = 30;

    for (let i = 0; i < MAX_ATTEMPTS; i++) {
        if (signal.aborted) throw new Error("aborted");
        if (isOnSolidGround(bot)) return;

        const pos = bot.entity.position.floored();

        // Check Head, Feet, and Below to clear a vertical falling shaft
        const blocksToClear = [
            bot.blockAt(pos.offset(0, 1, 0)), // Head (prevents suffocation lock)
            bot.blockAt(pos), // Torso (if clipped inside a block)
            bot.blockAt(pos.offset(0, -1, 0)), // Below (to fall down)
        ];

        let clearedSomething = false;

        for (const targetBlock of blocksToClear) {
            // If the block exists, is a tree part, and isn't air
            if (
                targetBlock &&
                TREE_BLOCKS.has(targetBlock.name) &&
                targetBlock.name !== "air"
            ) {
                try {
                    // Equip the right tool (axes for logs, hands/swords for leaves)
                    const tool = bot.pathfinder.bestHarvestTool(targetBlock);
                    if (tool) await bot.equip(tool, "hand");

                    // forceLook = true: Mineflayer sometimes fails reach-checks if the bot
                    // is clipped inside the block. This forces the break.
                    await bot.dig(targetBlock, true);
                    clearedSomething = true;
                } catch (err) {
                    // Ignore specific dig errors, we'll try to wiggle out below
                }
            }
        }

        // If we are stuck on a branch edge and couldn't dig anything directly below/inside us:
        if (!clearedSomething && !isOnSolidGround(bot)) {
            // Do a tiny random wiggle to fall off the edge of the leaf/branch
            const dir = Math.random() > 0.5 ? "forward" : "back";
            bot.setControlState(dir, true);
            await new Promise((res) => setTimeout(res, 250));
            bot.setControlState(dir, false);
        }

        // Wait a moment for gravity to pull the bot down after breaking the block
        await new Promise<void>((resolve) => {
            let ticks = 0;
            const check = () => {
                ticks++;
                if (isOnSolidGround(bot) || ticks > 10) {
                    bot.removeListener("physicsTick", check);
                    resolve();
                }
            };
            bot.on("physicsTick", check);

            // Fallback timeout just in case physics ticks lag
            setTimeout(() => {
                bot.removeListener("physicsTick", check);
                resolve();
            }, 1000);
        });
    }

    if (!isOnSolidGround(bot)) {
        throw new Error(
            "EscapeTree - could not reach ground after 30 attempts. Likely stuck in a massive canopy.",
        );
    }
}

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
    signal: AbortSignal;
    timeoutMs: number;
    stopMovement: () => void;
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
        stuckTimeoutMs = 5000,
    } = opts;

    return new Promise((resolve, reject) => {
        if (signal.aborted) {
            reject(new Error("aborted"));
            return;
        }

        if (goal.isEnd && goal.isEnd(bot.entity.position)) {
            return resolve();
        }

        let settled = false;
        let lastPos = bot.entity.position.clone();
        let stuckTimer: NodeJS.Timeout | null = null;

        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;

            clearTimeout(timer);
            if (stuckTimer) clearInterval(stuckTimer);

            signal.removeEventListener("abort", onAbort);
            bot.removeListener("goal_reached", onGoalReached);
            bot.removeListener("path_update", onPathUpdate);

            if (err) {
                reject(err);
                return;
            }
            resolve();
        };

        const safeStop = () => {
            try {
                bot.clearControlStates();
                stopMovement();
            } catch (_err) {}
        };

        const onAbort = () => {
            safeStop();
            finish(new Error("aborted"));
        };

        const onGoalReached = () => {
            safeStop();
            finish();
        };

        const onPathUpdate = (results: any) => {
            if (results.status === "noPath") {
                setTimeout(() => {
                    if (!settled) {
                        safeStop();
                        finish(new Error("no_path"));
                    }
                }, 3500);
            } else if (results.status === "timeout") {
                safeStop();
                finish(new Error("pathfinder_timeout"));
            }
        };

        const timer = setTimeout(() => {
            safeStop();
            finish(new Error("timeout"));
        }, timeoutMs);

        // Stuck detection loop with a Strike System
        let stuckStrikes = 0;

        stuckTimer = setInterval(() => {
            const currentPos = bot.entity.position;
            const dist = lastPos.distanceTo(currentPos);

            // 0.5 blocks over 5 seconds can trigger false positives in water or dense jungles.
            if (dist < 0.5) {
                stuckStrikes++;
                // Require 2 consecutive strikes (10 seconds total) before aborting
                if (stuckStrikes >= 2) {
                    safeStop();
                    finish(new Error("stuck"));
                }
            } else {
                // Reset strikes if the bot makes meaningful progress
                stuckStrikes = 0;
                lastPos = currentPos.clone();
            }
        }, stuckTimeoutMs);

        signal.addEventListener("abort", onAbort, { once: true });
        bot.once("goal_reached", onGoalReached);
        bot.on("path_update", onPathUpdate);

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

// ==========================================
// INVENTORY MANAGEMENT
// ==========================================

export async function makeRoomInInventory(
    bot: any,
    slotsNeeded: number = 1,
): Promise<void> {
    if (bot.inventory.emptySlotCount() >= slotsNeeded) return;

    const inventory = bot.inventory.items();
    const sortedItems = [...inventory].sort((a, b) => {
        const aIsTrash = TRASH_ITEMS.has(a.name) ? -1 : 1;
        const bIsTrash = TRASH_ITEMS.has(b.name) ? -1 : 1;
        if (aIsTrash !== bIsTrash) return aIsTrash - bIsTrash;
        return a.count - b.count;
    });

    let slotsFreed = 0;
    for (const item of sortedItems) {
        if (slotsFreed >= slotsNeeded) break;
        try {
            await bot.tossStack(item);
            slotsFreed++;
        } catch (err) {
            // Ignore failure, try next item
        }
    }
}

// ==========================================
// BLOCK PLACEMENT
// ==========================================

export async function placePortableUtility(
    bot: any,
    blockName: string,
): Promise<any> {
    const item = bot.inventory.items().find((i: any) => i.name === blockName);
    if (!item) return null;

    await bot.equip(item, "hand");

    const pos = bot.entity.position.floored();
    const candidates = [
        bot.blockAt(pos.offset(1, -1, 0)),
        bot.blockAt(pos.offset(-1, -1, 0)),
        bot.blockAt(pos.offset(0, -1, 1)),
        bot.blockAt(pos.offset(0, -1, -1)),
    ];

    for (const refBlock of candidates) {
        if (refBlock && refBlock.name !== "air" && refBlock.name !== "water") {
            try {
                await bot.placeBlock(refBlock, new Vec3(0, 1, 0));
                return findNearestBlockByName(bot, blockName);
            } catch (err) {
                // Ignore placement failure, check next adjacent block
            }
        }
    }
    return null;
}
