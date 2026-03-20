// js/lib/tasks/task.ts
import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import * as models from "../models.js";

const { goals } = pkg;

// ==========================================
// TASK HELPERS
// ==========================================

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

        const onAbort = () => {
            bot.clearControlStates();
            stopMovement();
            finish(new Error("aborted"));
        };

        const onGoalReached = () => {
            bot.clearControlStates();
            stopMovement();
            finish();
        };

        const onPathUpdate = (results: any) => {
            if (results.status === "noPath") {
                bot.clearControlStates();
                stopMovement();
                finish(new Error("NoPath - Bot is likely trapped"));
            } else if (results.status === "timeout") {
                bot.clearControlStates();
                stopMovement();
                finish(
                    new Error("PathfinderTimeout - Calculation took too long"),
                );
            }
        };

        const timer = setTimeout(() => {
            bot.clearControlStates();
            stopMovement();
            finish(new Error("timeout"));
        }, timeoutMs);

        signal.addEventListener("abort", onAbort, { once: true });
        bot.once("goal_reached", onGoalReached);
        bot.on("path_update", onPathUpdate);

        try {
            bot.pathfinder.setGoal(goal);
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

        case "retreat": {
            const threats = getThreats();
            const safePos = computeSafeRetreat(threats);

            await moveToGoal(
                bot,
                new goals.GoalNear(
                    safePos.x,
                    bot.entity.position?.y ?? 64,
                    safePos.z,
                    2,
                ),
                signal,
                timeouts.retreat ?? 15000,
                stopMovement,
            );
            return;
        }

        case "gather": {
            const targetBlockName = decision.target?.name;
            if (!targetBlockName || targetBlockName === "none") {
                throw new Error("missing gather target");
            }

            const block = findNearestBlockByName(bot, targetBlockName);
            if (!block) {
                throw new Error(`block not found: ${targetBlockName}`);
            }

            // Path to block
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

            // Attempt to equip the best tool before digging (fail silently if none exists)
            try {
                const tool = bot.pathfinder.bestHarvestTool(block);
                if (tool) await bot.equip(tool, "hand");
            } catch {}

            await bot.dig(block);

            // Wait for item to spawn and pick it up
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
                ).catch(() => {}); // Ignore failure, it might have been picked up mid-pathing
            }
            return;
        }

        case "craft": {
            const targetRecipeName = decision.target?.name;
            if (!targetRecipeName || targetRecipeName === "none") {
                throw new Error("missing craft target");
            }

            const itemType = bot.registry.itemsByName[targetRecipeName];
            if (!itemType) {
                throw new Error(`unknown item: ${targetRecipeName}`);
            }

            const craftingTable = findNearestBlockByName(bot, "crafting_table");
            const recipes = bot.recipesFor(itemType.id, null, 1, craftingTable);

            if (recipes.length === 0) {
                throw new Error(
                    `no valid recipe or missing ingredients for ${targetRecipeName}`,
                );
            }

            const recipe = recipes[0];

            if (recipe.requiresTable) {
                if (!craftingTable) {
                    throw new Error(
                        "requires crafting table but none are nearby",
                    );
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
            return;
        }

        case "hunt": {
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
                        // Target died or despawned. Look for dropped loot.
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
                        // Equip best weapon
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
                    } catch {}
                }

                await waitForMs(500, signal); // Wait for hit cooldown
            }

            throw new Error("timeout");
        }

        case "explore": {
            // Pick a random point ~30 blocks away to uncover new chunks
            const angle = Math.random() * Math.PI * 2;
            const targetX = bot.entity.position.x + Math.cos(angle) * 30;
            const targetZ = bot.entity.position.z + Math.sin(angle) * 30;

            await moveToGoal(
                bot,
                new goals.GoalNearXZ(targetX, targetZ, 2),
                signal,
                timeouts.explore ?? 20000,
                stopMovement,
            );
            return;
        }

        default:
            throw new Error(`unsupported action: ${decision.action}`);
    }
}
