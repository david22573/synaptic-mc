// js/lib/tasks/task.ts
import type { Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";
import * as models from "../models.js";
import { type TaskContext, INTENT_EVALUATORS } from "./registry.js";
import { log } from "../logger.js";
import { BotController } from "../control/controller.js";

interface BotWithController extends Bot {
    controller: BotController;
}

import { handleGather } from "./handlers/gather.js";
import { handleHunt } from "./handlers/hunt.js";
import { handleCraft } from "./handlers/craft.js";
import { handleBuild } from "./handlers/build.js";
import { handleExplore } from "./handlers/explore.js";
import { handleSmelt } from "./handlers/smelt.js";
import { handleMine } from "./handlers/mine.js";
import { handleFarm } from "./handlers/farm.js";
import { escapeTree, moveToGoal, waitForMs } from "./utils.js";
import { normalizeIntent } from "./normalize.js";
import { handleInteract } from "./handlers/interact.js";
import { handleStore } from "./handlers/store.js";
import { handleRetrieve } from "./handlers/retrieve.js";
import { handleUseSkill } from "./handlers/use_skill.js";

import { navigateWithFallbacks } from "../movement/navigator.js";
import { ExecutionError } from "../errors.js";
import { Vec3 } from "vec3";

const { goals } = pkg;

export interface ExecutionResult {
    success: boolean;
    cause: string;
    progress: number;
}

export function calculateDynamicTimeout(
    intent: models.ActionIntent,
    bot: Bot,
    baseTimeouts: Record<string, number>,
): number {
    const baseTimeout = baseTimeouts[intent.action] || 15000;
    const invCount = bot.inventory.items().length;
    const healthFactor = Math.max(bot.health, 1) / 20;

    return baseTimeout * (1 + invCount * 0.02) * (0.5 + healthFactor);
}

export async function runTask(
    bot: Bot,
    rawIntent: models.ActionIntent,
    signal: AbortSignal,
    timeouts: Record<string, number>,
    getThreats: () => models.ThreatInfo[],
    computeSafeRetreat: (threats: models.ThreatInfo[]) => {
        x: number;
        z: number;
    },
    stopMovement: () => void,
): Promise<ExecutionResult> {
    const intent = normalizeIntent(bot, rawIntent);

    const dynamicTimeouts = { ...timeouts };
    dynamicTimeouts[intent.action] = calculateDynamicTimeout(
        intent,
        bot,
        timeouts,
    );

    const taskCtx: TaskContext = {
        bot,
        intent,
        signal,
        timeouts: dynamicTimeouts,
        getThreats,
        computeSafeRetreat,
        stopMovement,
    };

    if (signal.aborted) {
        return { success: false, cause: "aborted", progress: 0.0 };
    }

    try {
        // --- COMPOSITE TASK RUNNER ---
        const extIntent = intent as any;
        const steps = extIntent.skill_steps || extIntent.skillSteps;
        if (steps && Array.isArray(steps) && steps.length > 0) {
            log.info(
                `Executing composite task '${intent.action}' with ${steps.length} steps`,
            );
            let progressAccum = 0;

            for (let i = 0; i < steps.length; i++) {
                if (signal.aborted) {
                    return {
                        success: false,
                        cause: "aborted",
                        progress: progressAccum,
                    };
                }

                const step = steps[i];
                log.info(
                    `[Step ${i + 1}/${steps.length}] Starting sub-task: ${step.action}`,
                );

                const stepResult = await runTask(
                    bot,
                    step,
                    signal,
                    timeouts,
                    getThreats,
                    computeSafeRetreat,
                    stopMovement,
                );

                if (!stepResult.success) {
                    log.warn(
                        `[Step ${i + 1}/${steps.length}] Sub-task failed`,
                        { cause: stepResult.cause },
                    );
                    return {
                        success: false,
                        cause: `step_failed:${step.action}:${stepResult.cause}`,
                        progress: (i + stepResult.progress) / steps.length,
                    };
                }
                progressAccum = (i + 1) / steps.length;
            }
            return { success: true, cause: "", progress: 1.0 };
        }

        // --- CONTINUOUS LOOP BRIDGE ---
        if (INTENT_EVALUATORS[intent.action]) {
            const controller = (bot as BotWithController).controller;
            if (!controller) {
                throw new ExecutionError(
                    "BotController not initialized on bot instance",
                    "error",
                    0,
                );
            }

            controller.setIntent(intent);

            return await new Promise<ExecutionResult>((resolve) => {
                const check = setInterval(() => {
                    if (signal.aborted) {
                        clearInterval(check);
                        controller.setIntent({
                            action: "idle",
                            target: { name: "none", type: "location" },
                        } as models.ActionIntent);
                        resolve({
                            success: false,
                            cause: "aborted",
                            progress: 0.0,
                        });
                        return;
                    }
                    const state = controller.intentState;
                    if (state.completed) {
                        clearInterval(check);
                        resolve({
                            success: true,
                            cause: "completed",
                            progress: 1.0,
                        });
                    } else if (state.failed) {
                        clearInterval(check);
                        resolve({
                            success: false,
                            cause: state.reason || "failed",
                            progress: 0.0,
                        });
                    }
                }, 100);
            });
        }

        // --- LEGACY FSM ROUTER ---
        switch (intent.action) {
            case "gather":
                await handleGather(taskCtx);
                break;
            case "hunt":
                await handleHunt(taskCtx);
                break;
            case "craft":
                await handleCraft(taskCtx);
                break;
            case "build":
                await handleBuild(taskCtx);
                break;
            case "smelt":
                await handleSmelt(taskCtx);
                break;
            case "mine":
                await handleMine(taskCtx);
                break;
            case "farm":
                await handleFarm(taskCtx);
                break;
            case "explore":
                await handleExplore(taskCtx);
                break;
            case "store":
                await handleStore(taskCtx);
                break;
            case "retrieve":
                await handleRetrieve(taskCtx);
                break;
            case "camera_move": {
                const data = JSON.parse(intent.target.name);
                await bot.look(data.yaw, data.pitch, true);
                break;
            }
            case "eat": {
                const food = bot.inventory
                    .items()
                    .find((i) => i.name === intent.target.name);
                if (!food) throw new ExecutionError(`NO_FOOD`, "error", 0);
                try {
                    await bot.equip(food.type, "hand");
                    await bot.consume();
                } catch (err) {
                    throw new ExecutionError(`CONSUME_FAILED`, "error", 0);
                }
                break;
            }
            case "idle":
                await waitForMs(1500, signal);
                break;
            case "look": {
                if (intent.target.type === "location") {
                    try {
                        const data = JSON.parse(intent.target.name);
                        if (data.x !== undefined) {
                            await bot.lookAt(
                                new Vec3(data.x, data.y || 64, data.z),
                                true,
                            );
                        } else if (data.yaw !== undefined) {
                            await bot.look(data.yaw, data.pitch || 0, true);
                        }
                    } catch (e) {
                        const yawMap: Record<string, number> = {
                            north: Math.PI,
                            south: 0,
                            east: Math.PI / 2,
                            west: -Math.PI / 2,
                        };
                        if (
                            yawMap[intent.target.name.toLowerCase()] !==
                            undefined
                        ) {
                            await bot.look(
                                yawMap[intent.target.name.toLowerCase()],
                                0,
                                true,
                            );
                        }
                    }
                } else if (intent.target.type === "entity") {
                    const e = bot.nearestEntity(
                        (e) => e.name === intent.target.name,
                    );
                    if (e) {
                        await bot.lookAt(
                            e.position.offset(0, e.height || 1.5, 0),
                            true,
                        );
                    }
                } else {
                    const startYaw = bot.entity.yaw;
                    await bot.look(startYaw + Math.PI / 4, 0, true);
                    await waitForMs(400, signal);
                    await bot.look(startYaw - Math.PI / 4, 0, true);
                    await waitForMs(400, signal);
                    await bot.look(startYaw, 0, true);
                }
                break;
            }
            case "sleep": {
                await escapeTree(bot, signal);
                const bed = bot.findBlock({
                    maxDistance: 32,
                    matching: (b: any) => b?.name.includes("bed"),
                });

                if (!bed) throw new ExecutionError("no bed found", "error", 0);

                await navigateWithFallbacks(
                    bot,
                    new goals.GoalNear(
                        bed.position.x,
                        bed.position.y,
                        bed.position.z,
                        1.5,
                    ),
                    { signal, timeoutMs: 20000, stopMovement },
                );

                let onWake: (() => void) | undefined;
                let onAbort: (() => void) | undefined;
                let timeoutId: NodeJS.Timeout | undefined;

                try {
                    const wakePromise = new Promise<void>((resolve, reject) => {
                        onWake = resolve;
                        onAbort = () =>
                            reject(
                                new ExecutionError("aborted", "aborted", 1.0),
                            );
                        bot.on("wake", onWake);
                        signal.addEventListener("abort", onAbort, {
                            once: true,
                        });
                    });

                    const sleepAbortCtrl = new AbortController();
                    const timeoutPromise = new Promise<void>((_, reject) => {
                        timeoutId = setTimeout(() => {
                            sleepAbortCtrl.abort();
                            reject(
                                new ExecutionError(
                                    "sleep timeout",
                                    "timeout",
                                    1.0,
                                ),
                            );
                        }, 12000);
                    });

                    const sleepPromise = bot.sleep(bed).then(() => wakePromise);
                    await Promise.race([sleepPromise, timeoutPromise]);
                } finally {
                    if (timeoutId) clearTimeout(timeoutId);
                    if (onWake) bot.removeListener("wake", onWake);
                    if (onAbort) signal.removeEventListener("abort", onAbort);
                }
                break;
            }
            case "retreat": {
                await escapeTree(bot, signal);
                const threats = taskCtx.getThreats ? taskCtx.getThreats() : [];
                const pos = taskCtx.computeSafeRetreat
                    ? taskCtx.computeSafeRetreat(threats)
                    : {
                          x: bot.entity.position.x + 5,
                          z: bot.entity.position.z + 5,
                      };

                log.info("Retreating to safe position", {
                    threatCount: threats.length,
                    target: pos,
                });

                // Force sprint and jump for maximum escape speed
                bot.setControlState("sprint", true);
                bot.setControlState("jump", true);

                await navigateWithFallbacks(
                    bot,
                    new goals.GoalNearXZ(pos.x, pos.z, 2),
                    {
                        signal,
                        timeoutMs: 30000, // Increased timeout for long retreats
                        stopMovement,
                    },
                );
                bot.setControlState("sprint", false);
                bot.setControlState("jump", false);
                await waitForMs(1000, signal);
                break;
            }
            case "interact":
                await handleInteract(taskCtx);
                break;
            case "mark_location":
            case "recall_location":
                await waitForMs(500, signal);
                break;
            case "use_skill":
                await handleUseSkill(taskCtx);
                break;
            default:
                throw new ExecutionError(
                    `unsupported: ${intent.action}`,
                    "error",
                    0,
                );
        }

        return { success: true, cause: "", progress: 1.0 };
    } catch (err: any) {
        stopMovement();

        if (err?.message === "aborted") {
            return { success: false, cause: "aborted", progress: 1.0 };
        }

        if (err instanceof ExecutionError || err.name === "ExecutionError") {
            return {
                success: err.cause === "partial",
                cause: err.cause,
                progress: err.progress,
            };
        }

        log.error(`Task handler error in ${intent.action}`, {
            error: err.message,
            stack: err.stack,
        });
        return { success: false, cause: "error", progress: 0.0 };
    }
}
