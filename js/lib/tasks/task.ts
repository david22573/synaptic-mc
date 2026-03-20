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

        let settled = false;

        const cleanup = () => {
            clearTimeout(timer);
            signal.removeEventListener("abort", onAbort);
            bot.removeListener("goal_reached", onGoalReached);
        };

        const finish = (err?: Error) => {
            if (settled) return;
            settled = true;
            cleanup();

            if (err) {
                reject(err);
                return;
            }

            resolve();
        };

        const onAbort = () => {
            stopMovement();
            finish(new Error("aborted"));
        };

        const onGoalReached = () => {
            stopMovement();
            finish();
        };

        const timer = setTimeout(() => {
            stopMovement();
            finish(new Error("timeout"));
        }, timeoutMs);

        signal.addEventListener("abort", onAbort, { once: true });
        bot.once("goal_reached", onGoalReached);
        bot.pathfinder.setGoal(goal);
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

        case "attack": {
            const targetName = decision.target?.name;
            if (!targetName || targetName === "none") {
                throw new Error("missing attack target");
            }

            const attackStartedAt = Date.now();
            let hasSeenTarget = false;
            let lastSeenAt = 0;

            while (Date.now() - attackStartedAt < (timeouts.attack ?? 20000)) {
                if (signal.aborted) {
                    throw new Error("aborted");
                }

                const targetEntity = bot.nearestEntity(
                    (e: any) => e.name === targetName && e.isValid,
                );

                if (!targetEntity) {
                    if (hasSeenTarget && Date.now() - lastSeenAt > 1500) {
                        stopMovement();
                        return;
                    }

                    if (!hasSeenTarget && Date.now() - attackStartedAt > 3000) {
                        throw new Error(`target not found: ${targetName}`);
                    }

                    await waitForMs(250, signal);
                    continue;
                }

                hasSeenTarget = true;
                lastSeenAt = Date.now();

                bot.pathfinder.setGoal(
                    new goals.GoalFollow(targetEntity, 2),
                    true,
                );
                const dist = bot.entity.position.distanceTo(
                    targetEntity.position,
                );

                if (dist < 3.2) {
                    try {
                        await bot.lookAt(
                            targetEntity.position.offset(
                                0,
                                targetEntity.height ?? 1.6,
                                0,
                            ),
                            true,
                        );
                    } catch {}

                    try {
                        bot.attack(targetEntity);
                    } catch {}
                }

                await waitForMs(450, signal);
            }

            throw new Error("timeout");
        }

        case "mine": {
            const targetBlockName = decision.target?.name;
            if (!targetBlockName || targetBlockName === "none") {
                throw new Error("missing mine target");
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
                    1,
                ),
                signal,
                timeouts.mine ?? 20000,
                stopMovement,
            );

            if (signal.aborted) {
                throw new Error("aborted");
            }

            if (!bot.canDigBlock(block)) {
                throw new Error(`cannot dig block: ${targetBlockName}`);
            }

            await bot.dig(block);
            return;
        }

        default:
            throw new Error(`unsupported action: ${decision.action}`);
    }
}
