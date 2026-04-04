// js/lib/tasks/handlers/explore.ts
import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";
import { navigateWithFallbacks } from "../../movement/navigator.js";
import { log } from "../../logger.js";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";

const { goals } = pkg;

const MAX_HISTORY_POINTS = 50;

interface ExploreContext extends StateContext {
    targetX: number;
    targetZ: number;
    attempts: number;
    stopMovement: () => void;
    visitedPoints: { x: number; z: number }[];
}

class NavigateState implements FSMState {
    name = "NAVIGATING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const eCtx = ctx as ExploreContext;

        try {
            await navigateWithFallbacks(
                eCtx.bot,
                new goals.GoalNearXZ(eCtx.targetX, eCtx.targetZ, 2),
                {
                    signal: eCtx.signal,
                    timeoutMs: 12000,
                    stopMovement: eCtx.stopMovement,
                    maxRetries: 2,
                },
            );

            eCtx.visitedPoints.push({
                x: eCtx.bot.entity.position.x,
                z: eCtx.bot.entity.position.z,
            });

            while (eCtx.visitedPoints.length > MAX_HISTORY_POINTS) {
                eCtx.visitedPoints.shift();
            }

            eCtx.result = { status: "SUCCESS", reason: "EXPLORED_TARGET_AREA" };
            return null;
        } catch (err: any) {
            const msg = String(err?.message ?? err);

            if (eCtx.signal.aborted || msg.includes("aborted")) {
                eCtx.result = { status: "FAILED", reason: "aborted" };
                return null;
            }

            log.warn("Exploration path failed, picking new direction", {
                reason: msg,
            });

            return new PickDirectionState();
        }
    }
}

class PickDirectionState implements FSMState {
    name = "PICKING_DIRECTION";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const eCtx = ctx as ExploreContext;
        eCtx.attempts++;

        if (eCtx.attempts > 3) {
            eCtx.result = {
                status: "FAILED",
                reason: "SURROUNDED_BY_OBSTACLES_OR_WATER",
            };
            return null;
        }

        const dist = 12 + Math.random() * 8;
        let bestTarget = null;
        let maxRepulsionScore = -1;

        const pos = eCtx.bot.entity.position.floored();

        for (let i = 0; i < 12; i++) {
            // Yield to the event loop to prevent blocking physics and network ticks
            if (i > 0 && i % 3 === 0) {
                await new Promise((r) => setTimeout(r, 0));
            }

            if (eCtx.signal.aborted) throw new Error("aborted");

            const testAngle = Math.random() * Math.PI * 2;
            let tx = pos.x;
            let tz = pos.z;
            let ty = pos.y;
            let validLength = 0;

            for (let step = 1; step <= dist; step++) {
                const checkX = Math.floor(pos.x + Math.cos(testAngle) * step);
                const checkZ = Math.floor(pos.z + Math.sin(testAngle) * step);

                let foundSurface = false;

                for (let yOffset = 2; yOffset >= -2; yOffset--) {
                    const block = eCtx.bot.blockAt(
                        new Vec3(checkX, ty + yOffset, checkZ),
                    );
                    const blockAbove = eCtx.bot.blockAt(
                        new Vec3(checkX, ty + yOffset + 1, checkZ),
                    );
                    const blockAbove2 = eCtx.bot.blockAt(
                        new Vec3(checkX, ty + yOffset + 2, checkZ),
                    );

                    if (!block || !blockAbove || !blockAbove2) break;

                    if (
                        block.boundingBox === "block" &&
                        blockAbove.name === "air" &&
                        blockAbove2.name === "air" &&
                        block.name !== "water" &&
                        block.name !== "lava" &&
                        block.name !== "magma_block"
                    ) {
                        tx = checkX;
                        tz = checkZ;
                        ty = ty + yOffset;
                        validLength = step;
                        foundSurface = true;
                        break;
                    }
                }
                if (!foundSurface) break;
            }

            if (validLength < 2) continue;

            let minDistanceToHistory = 999999;
            for (const pt of eCtx.visitedPoints) {
                const d = Math.sqrt((tx - pt.x) ** 2 + (tz - pt.z) ** 2);
                if (d < minDistanceToHistory) minDistanceToHistory = d;
            }
            if (eCtx.visitedPoints.length === 0)
                minDistanceToHistory = validLength;

            if (minDistanceToHistory > maxRepulsionScore) {
                maxRepulsionScore = minDistanceToHistory;
                bestTarget = { x: tx, z: tz };
            }
        }

        if (!bestTarget) {
            log.warn(
                "Exploration raymarch failed, picking random fallback target",
                { attempt: eCtx.attempts },
            );
            bestTarget = {
                x: pos.x + (Math.random() * 16 - 8),
                z: pos.z + (Math.random() * 16 - 8),
            };
        }

        eCtx.targetX = bestTarget.x;
        eCtx.targetZ = bestTarget.z;

        log.info("Picked exploration vector", {
            targetX: Math.round(eCtx.targetX),
            targetZ: Math.round(eCtx.targetZ),
            attempt: eCtx.attempts,
        });

        return new NavigateState();
    }
}

export async function handleExplore(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;

    try {
        await escapeTree(bot, signal);

        const botRef = bot as any;
        botRef.explorationHistory = botRef.explorationHistory || [];

        const fsmCtx: ExploreContext = {
            bot,
            targetName: "explore",
            targetEntity: null,
            searchRadius: 0,
            timeoutMs: timeouts.explore ?? 25000,
            startTime: 0,
            signal,
            targetX: 0,
            targetZ: 0,
            attempts: 0,
            stopMovement,
            visitedPoints: botRef.explorationHistory,
        };

        await new StateMachineRunner(new PickDirectionState(), fsmCtx).run();
    } catch (err: any) {
        log.warn("Explore task aborted or crashed", { reason: err.message });
    }
}
