import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import * as models from "../models.js";
import { Vec3 } from "vec3";

const { goals } = pkg;

// ==========================================
// TASK HELPERS
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

function isOnSolidGround(bot: any): boolean {
    if (!bot.entity.onGround) return false;
    const pos = bot.entity.position.floored();
    const below = bot.blockAt(pos.offset(0, -1, 0));
    return !!below && !TREE_BLOCKS.has(below.name) && below.name !== "air";
}

function isInFoliage(bot: any): boolean {
    const pos = bot.entity.position.floored();
    const at = bot.blockAt(pos);
    const below = bot.blockAt(pos.offset(0, -1, 0));

    // Removed the buggy jump/fall check
    return (
        (at && TREE_BLOCKS.has(at.name)) ||
        (below && TREE_BLOCKS.has(below.name))
    );
}

export async function escapeTree(bot: any, signal: AbortSignal): Promise<void> {
    if (!isInFoliage(bot)) return;

    try {
        bot.pathfinder.setGoal(null);
    } catch (_err) {}

    try {
        bot.clearControlStates();
    } catch (_err) {}

    const MAX_ATTEMPTS = 16;
    for (let i = 0; i < MAX_ATTEMPTS; i++) {
        if (signal.aborted) throw new Error("aborted");

        if (isOnSolidGround(bot)) return;

        const pos = bot.entity.position.floored();
        const below = bot.blockAt(pos.offset(0, -1, 0));

        if (below && TREE_BLOCKS.has(below.name) && bot.canDigBlock(below)) {
            try {
                await bot.dig(below);
            } catch (_err) {
                // Ignore dig errors during a panic escape
            }
        }

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
            setTimeout(() => {
                bot.removeListener("physicsTick", check);
                resolve();
            }, 1500);
        });
    }

    if (!isOnSolidGround(bot))
        throw new Error("EscapeTree - could not reach ground");
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

export function moveToGoal(
    bot: any,
    goal: any,
    signal: AbortSignal,
    timeoutMs: number,
    stopMovement: () => void,
): Promise<void> {
    return new Promise((resolve, reject) => {
        if (signal.aborted) {
            reject(new Error("aborted"));
            return;
        }

        if (goal.isEnd && goal.isEnd(bot.entity.position)) {
            return resolve();
        }

        let settled = false;

        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;

            clearTimeout(timer);
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
            } catch (_err) {
                // Ignore cleanup errors during teardown
            }
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
                        finish(new Error("NoPath - Bot is likely trapped"));
                    }
                }, 3500);
            } else if (results.status === "timeout") {
                safeStop();
                finish(
                    new Error("PathfinderTimeout - Calculation took too long"),
                );
            }
        };

        const timer = setTimeout(() => {
            safeStop();
            finish(new Error("timeout"));
        }, timeoutMs);

        signal.addEventListener("abort", onAbort, { once: true });
        bot.once("goal_reached", onGoalReached);
        bot.on("path_update", onPathUpdate);

        try {
            // true enables dynamic pathfinding to stop computational freezes
            bot.pathfinder.setGoal(goal, true);
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

// Drops low value items to make space, preventing tool loss
async function makeRoomInInventory(
    bot: any,
    slotsNeeded: number = 1,
): Promise<void> {
    if (bot.inventory.emptySlotCount() >= slotsNeeded) return;

    const inventory = bot.inventory.items();
    const trashNames = [
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
    ];

    const sortedItems = [...inventory].sort((a, b) => {
        const aIsTrash = trashNames.includes(a.name) ? -1 : 1;
        const bIsTrash = trashNames.includes(b.name) ? -1 : 1;
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

async function placePortableUtility(bot: any, blockName: string): Promise<any> {
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

// ==========================================
// TASK EXECUTION
// ==========================================

export async function runTask(
    bot: any,
    decision: models.IncomingDecision,
    signal: AbortSignal,
    timeouts: Record<string, number>,
    getThreats: () => models.ThreatInfo[],
    computeSafeRetreat: (threats: models.ThreatInfo[]) => {
        x: number;
        z: number;
    },
    stopMovement: () => void,
): Promise<void> {
    switch (decision.action) {
        case "idle": {
            await waitForMs(1500, signal);
            return;
        }

        case "build": {
            await escapeTree(bot, signal);

            const targetBlockName = decision.target?.name;
            if (!targetBlockName || targetBlockName === "none") {
                throw new Error("missing build target");
            }

            const item = bot.inventory
                .items()
                .find((i: any) => i.name === targetBlockName);

            if (!item) {
                throw new Error(`missing ${targetBlockName} in inventory`);
            }

            const placedBlock = await placePortableUtility(
                bot,
                targetBlockName,
            );

            if (!placedBlock) {
                throw new Error(
                    `could not find a valid surface to place ${targetBlockName}`,
                );
            }

            await waitForMs(500, signal);
            return;
        }

        case "sleep": {
            await escapeTree(bot, signal);

            const bed = bot.findBlock({
                maxDistance: 32,
                matching: (block: any) => block?.name.includes("bed"),
            });

            if (!bed) {
                throw new Error("no bed found nearby");
            }

            await moveToGoal(
                bot,
                new goals.GoalNear(
                    bed.position.x,
                    bed.position.y,
                    bed.position.z,
                    1.5,
                ),
                signal,
                20000,
                stopMovement,
            );

            if (signal.aborted) throw new Error("aborted");

            try {
                await bot.sleep(bed);
            } catch (err) {
                const msg = err instanceof Error ? err.message : String(err);
                if (
                    msg.includes("It's not night") ||
                    msg.includes("can't sleep")
                ) {
                    return;
                }
                throw new Error(`sleep interaction failed: ${msg}`);
            }
            return;
        }

        case "retreat": {
            await escapeTree(bot, signal);

            const threats = getThreats();
            const safePos = computeSafeRetreat(threats);

            await moveToGoal(
                bot,
                new goals.GoalNearXZ(safePos.x, safePos.z, 2),
                signal,
                timeouts.retreat ?? 15000,
                stopMovement,
            );
            return;
        }

        case "gather": {
            await escapeTree(bot, signal);

            const targetBlockName = decision.target?.name;
            if (!targetBlockName || targetBlockName === "none") {
                throw new Error("missing gather target");
            }

            const block = findNearestBlockByName(bot, targetBlockName);

            if (!block) {
                throw new Error(`block not found: ${targetBlockName}`);
            }

            await moveToGoal(
                bot,
                new goals.GoalNear(
                    block.position.x,
                    block.position.y,
                    block.position.z,
                    1.5,
                ),
                signal,
                timeouts.gather ?? 30000,
                stopMovement,
            );

            if (signal.aborted) throw new Error("aborted");

            if (!bot.canDigBlock(block)) {
                throw new Error(`cannot dig block: ${targetBlockName}`);
            }

            try {
                const tool = bot.pathfinder.bestHarvestTool(block);
                if (tool) await bot.equip(tool, "hand");
            } catch (_err) {}

            await bot.dig(block);
            await waitForMs(500, signal);

            const drop = bot.nearestEntity(
                (e: any) =>
                    e.name === "item" &&
                    e.position.distanceTo(block.position) < 3,
            );

            if (drop) {
                await moveToGoal(
                    bot,
                    new goals.GoalFollow(drop, 0),
                    signal,
                    3000,
                    stopMovement,
                ).catch(() => {});
            }
            return;
        }

        case "craft": {
            await escapeTree(bot, signal);

            const targetRecipeName = decision.target?.name;
            if (!targetRecipeName || targetRecipeName === "none") {
                throw new Error("missing craft target");
            }

            const itemType = bot.registry.itemsByName[targetRecipeName];

            if (!itemType) {
                throw new Error(`unknown item: ${targetRecipeName}`);
            }

            let craftingTable = findNearestBlockByName(bot, "crafting_table");
            let isPortableTable = false;

            const recipes = bot.recipesFor(itemType.id, null, 1, craftingTable);

            if (recipes.length === 0) {
                throw new Error(
                    `no valid recipe or missing ingredients for ${targetRecipeName}`,
                );
            }

            const recipe = recipes[0];

            if (recipe.requiresTable) {
                if (!craftingTable) {
                    craftingTable = await placePortableUtility(
                        bot,
                        "crafting_table",
                    );

                    if (!craftingTable) {
                        throw new Error(
                            "requires crafting table but none nearby or in inventory",
                        );
                    }
                    isPortableTable = true;
                }

                await moveToGoal(
                    bot,
                    new goals.GoalNear(
                        craftingTable.position.x,
                        craftingTable.position.y,
                        craftingTable.position.z,
                        2,
                    ),
                    signal,
                    timeouts.craft ?? 20000,
                    stopMovement,
                );
            }

            if (signal.aborted) throw new Error("aborted");

            await bot.craft(recipe, 1, craftingTable);

            if (isPortableTable && craftingTable) {
                await makeRoomInInventory(bot, 1);

                const pickaxe = bot.pathfinder.bestHarvestTool(craftingTable);
                if (pickaxe) await bot.equip(pickaxe, "hand");
                await bot.dig(craftingTable);
                await waitForMs(1000, signal);
            }
            return;
        }

        case "smelt": {
            await escapeTree(bot, signal);

            let furnace = findNearestBlockByName(bot, "furnace");
            let isPortableFurnace = false;

            if (!furnace) {
                furnace = await placePortableUtility(bot, "furnace");
                if (!furnace) {
                    throw new Error(
                        "requires furnace but none nearby or in inventory",
                    );
                }
                isPortableFurnace = true;
            }

            await moveToGoal(
                bot,
                new goals.GoalNear(
                    furnace.position.x,
                    furnace.position.y,
                    furnace.position.z,
                    2,
                ),
                signal,
                20000,
                stopMovement,
            );

            if (signal.aborted) throw new Error("aborted");

            const furnaceBlock = bot.openFurnace(furnace);

            try {
                const rawMeat = bot.inventory
                    .items()
                    .find((i: any) =>
                        [
                            "beef",
                            "porkchop",
                            "mutton",
                            "chicken",
                            "rabbit",
                        ].includes(i.name),
                    );

                const fuel = bot.inventory
                    .items()
                    .find((i: any) =>
                        ["coal", "charcoal", "oak_planks"].includes(i.name),
                    );

                if (rawMeat && fuel) {
                    await furnaceBlock.putFuel(fuel.type, null, 1);
                    await furnaceBlock.putInput(rawMeat.type, null, 1);
                    await waitForMs(11000, signal);
                    await furnaceBlock.takeOutput();
                }
            } finally {
                furnaceBlock.close();

                if (isPortableFurnace && furnace) {
                    await makeRoomInInventory(bot, 1);

                    const pickaxe = bot.pathfinder.bestHarvestTool(furnace);
                    if (pickaxe) await bot.equip(pickaxe, "hand");
                    await bot.dig(furnace);
                    await waitForMs(1000, signal);
                }
            }
            return;
        }

        case "hunt": {
            await escapeTree(bot, signal);

            const targetName = decision.target?.name;
            if (!targetName || targetName === "none") {
                throw new Error("missing hunt target");
            }

            const attackStartedAt = Date.now();
            let hasSeenTarget = false;

            while (Date.now() - attackStartedAt < (timeouts.hunt ?? 30000)) {
                if (signal.aborted) throw new Error("aborted");

                const targetEntity = bot.nearestEntity(
                    (e: any) => e.name === targetName && e.isValid,
                );

                if (!targetEntity) {
                    if (hasSeenTarget) {
                        const drop = bot.nearestEntity(
                            (e: any) =>
                                e.name === "item" &&
                                bot.entity.position.distanceTo(e.position) < 10,
                        );

                        if (drop) {
                            await moveToGoal(
                                bot,
                                new goals.GoalFollow(drop, 0),
                                signal,
                                5000,
                                stopMovement,
                            ).catch(() => {});
                        }
                        stopMovement();
                        return;
                    }
                    throw new Error(`target not found: ${targetName}`);
                }

                hasSeenTarget = true;

                bot.pathfinder.setGoal(
                    new goals.GoalFollow(targetEntity, 2),
                    true,
                );

                if (
                    bot.entity.position.distanceTo(targetEntity.position) < 3.2
                ) {
                    try {
                        const weapon = bot.inventory
                            .items()
                            .find(
                                (i: any) =>
                                    i.name.includes("sword") ||
                                    i.name.includes("axe"),
                            );

                        if (weapon) await bot.equip(weapon, "hand");

                        await bot.lookAt(
                            targetEntity.position.offset(
                                0,
                                targetEntity.height ?? 1.6,
                                0,
                            ),
                            true,
                        );
                        bot.attack(targetEntity);
                    } catch (_err) {}
                }

                await waitForMs(500, signal);
            }

            throw new Error("timeout");
        }

        case "explore": {
            await escapeTree(bot, signal);

            const angle = Math.random() * Math.PI * 2;
            const targetX = bot.entity.position.x + Math.cos(angle) * 30;
            const targetZ = bot.entity.position.z + Math.sin(angle) * 30;

            await moveToGoal(
                bot,
                new goals.GoalNearXZ(targetX, targetZ, 2),
                signal,
                timeouts.explore ?? 45000, // Make sure your Go config.json matches this bump
                stopMovement,
            );

            return;
        }

        default:
            throw new Error(`unsupported action: ${decision.action}`);
    }
}
