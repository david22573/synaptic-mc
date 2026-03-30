import { StateMachineRunner, } from "../fsm.js";
import {} from "../registry.js";
import { escapeTree, moveToGoal } from "../utils.js";
import { log } from "../../logger.js";
import pkg from "mineflayer-pathfinder";
const { goals } = pkg;
class NavigateState {
    name = "NAVIGATING";
    async enter() { }
    async execute(ctx) {
        const eCtx = ctx;
        try {
            await moveToGoal(eCtx.bot, new goals.GoalNearXZ(eCtx.targetX, eCtx.targetZ, 2), {
                signal: eCtx.signal,
                timeoutMs: 12000, // Reduced from 15s to fit within global limit better
                stopMovement: eCtx.stopMovement,
                dynamic: false,
            });
            // Successfully reached new area. Record it to repel future exploration.
            eCtx.visitedPoints.push({
                x: eCtx.bot.entity.position.x,
                z: eCtx.bot.entity.position.z,
            });
            if (eCtx.visitedPoints.length > 20)
                eCtx.visitedPoints.shift();
            eCtx.result = { status: "SUCCESS", reason: "EXPLORED_TARGET_AREA" };
            return null;
        }
        catch (err) {
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
class PickDirectionState {
    name = "PICKING_DIRECTION";
    async enter() { }
    async execute(ctx) {
        const eCtx = ctx;
        eCtx.attempts++;
        // Reduced from 4 attempts to 2 to prevent hitting global 25s FSM timeout
        if (eCtx.attempts > 2) {
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
                if (d < minDistanceToHistory)
                    minDistanceToHistory = d;
            }
            if (eCtx.visitedPoints.length === 0)
                minDistanceToHistory = 1;
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
export async function handleExplore(ctx) {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);
    // Bind the history array to the bot instance itself.
    const botRef = bot;
    botRef.explorationHistory = botRef.explorationHistory || [];
    const fsmCtx = {
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
    const result = await new StateMachineRunner(new PickDirectionState(), fsmCtx).run();
    if (result.status === "FAILED")
        throw new Error(result.reason || "unknown_fsm_failure");
}
//# sourceMappingURL=explore.js.map