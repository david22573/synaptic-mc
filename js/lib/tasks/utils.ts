import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";

const { goals } = pkg;

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
            const strafe = Math.random() > 0.5 ? "left" : "right";
            bot.setControlState(dir, true);
            bot.setControlState(strafe, true);
            await new Promise((res) => setTimeout(res, 250));
            bot.setControlState(dir, false);
            bot.setControlState(strafe, false);
        }

        await new Promise<void>((resolve, reject) => {
            let ticks = 0;
            const timeout = setTimeout(() => {
                bot.removeListener("physicsTick", check);
                resolve();
            }, 1000);

            const check = () => {
                ticks++;
                if (isOnSolidGround(bot) || ticks > 10) {
                    clearTimeout(timeout);
                    bot.removeListener("physicsTick", check);
                    resolve();
                }
            };
            bot.on("physicsTick", check);
        });
    }

    if (!isOnSolidGround(bot)) {
        throw new Error(
            "EscapeTree - could not reach ground after 30 attempts",
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
        const listeners = new Map<string, any>();

        const cleanup = () => {
            listeners.forEach((handler, event) =>
                bot.removeListener(event, handler),
            );
            listeners.clear();
            if (stuckTimer) clearInterval(stuckTimer);
        };

        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;
            cleanup();

            try {
                bot.clearControlStates();
                stopMovement();
            } catch (_err) {}

            if (err) {
                reject(err);
                return;
            }
            resolve();
        };

        const onAbort = () => finish(new Error("aborted"));
        const onGoalReached = () => finish();

        const onPathUpdate = (results: any) => {
            if (results.status === "noPath") {
                setTimeout(() => {
                    if (!settled) finish(new Error("no_path"));
                }, 3500);
            } else if (results.status === "timeout") {
                finish(new Error("pathfinder_timeout"));
            }
        };

        let stuckStrikes = 0;

        stuckTimer = setInterval(() => {
            const currentPos = bot.entity.position;
            const dist = lastPos.distanceTo(currentPos);
            if (dist < 0.5) {
                stuckStrikes++;
                if (stuckStrikes >= 2) {
                    finish(new Error("stuck"));
                }
            } else {
                stuckStrikes = 0;
                lastPos = currentPos.clone();
            }
        }, stuckTimeoutMs);

        listeners.set("goal_reached", onGoalReached);
        listeners.set("path_update", onPathUpdate);

        signal.addEventListener("abort", onAbort, { once: true });
        bot.once("goal_reached", onGoalReached);
        bot.on("path_update", onPathUpdate);

        const timer = setTimeout(() => finish(new Error("timeout")), timeoutMs);

        try {
            bot.pathfinder.setGoal(goal, dynamic);
        } catch (err) {
            clearTimeout(timer);
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

    // Pre-filter the inventory to only sort and process trash items
    const trashItems = inventory.filter((i: any) => TRASH_ITEMS.has(i.name));
    trashItems.sort((a: any, b: any) => a.count - b.count);

    let slotsFreed = 0;
    for (const item of trashItems) {
        if (slotsFreed >= slotsNeeded) break;
        try {
            await bot.tossStack(item);
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
                // Ignore placement failure
            }
        }
    }
    return null;
}
