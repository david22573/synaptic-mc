import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal } from "../utils.js";
import { log } from "../../logger.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

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
            await moveToGoal(
                eCtx.bot,
                new goals.GoalNearXZ(eCtx.targetX, eCtx.targetZ, 2),
                {
                    signal: eCtx.signal,
                    timeoutMs: 15000,
                    stopMovement: eCtx.stopMovement,
                    dynamic: false,
                },
            );

            // Successfully reached new area. Record it to repel future exploration.
            eCtx.visitedPoints.push({
                x: eCtx.bot.entity.position.x,
                z: eCtx.bot.entity.position.z,
            });
            if (eCtx.visitedPoints.length > 20) eCtx.visitedPoints.shift();

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
        if (eCtx.attempts > 4) {
            eCtx.result = {
                status: "FAILED",
                reason: "SURROUNDED_BY_OBSTACLES_OR_WATER",
            };
            return null;
        }

        const dist = 16 + Math.random() * 16;
        let bestAngle = 0;
        let maxRepulsionScore = -1;

        // Sample 8 angles and pick the one furthest from our visited history
        for (let i = 0; i < 8; i++) {
            const testAngle = Math.random() * Math.PI * 2;
            const tx = eCtx.bot.entity.position.x + Math.cos(testAngle) * dist;
            const tz = eCtx.bot.entity.position.z + Math.sin(testAngle) * dist;

            let minDistanceToHistory = 999999;
            for (const pt of eCtx.visitedPoints) {
                const d = Math.sqrt((tx - pt.x) ** 2 + (tz - pt.z) ** 2);
                if (d < minDistanceToHistory) minDistanceToHistory = d;
            }

            if (eCtx.visitedPoints.length === 0) minDistanceToHistory = 1;

            if (minDistanceToHistory > maxRepulsionScore) {
                maxRepulsionScore = minDistanceToHistory;
                bestAngle = testAngle;
            }
        }

        eCtx.targetX = eCtx.bot.entity.position.x + Math.cos(bestAngle) * dist;
        eCtx.targetZ = eCtx.bot.entity.position.z + Math.sin(bestAngle) * dist;

        log.info("Picked exploration vector", {
            dist: Math.round(dist),
            targetX: Math.round(eCtx.targetX),
            targetZ: Math.round(eCtx.targetZ),
            attempt: eCtx.attempts,
        });
        return new NavigateState();
    }
}

export async function handleExplore(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    // Bind the history array to the bot instance itself.
    // This way it persists across multiple 'explore' task calls in the same session,
    // but gets garbage collected naturally when the bot reconnects/re-instantiates.
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

    const result = await new StateMachineRunner(
        new PickDirectionState(),
        fsmCtx,
    ).run();

    if (result.status === "FAILED")
        throw new Error(result.reason || "unknown_fsm_failure");
}
