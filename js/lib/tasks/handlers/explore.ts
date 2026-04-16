import { type Bot } from "mineflayer";
import { type Entity } from "prismarine-entity";
import { type Block } from "prismarine-block";
import { ActionPlan, Perception } from "../../control/controller.js";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";

const { goals } = pkg;

const MAX_HISTORY_POINTS = 50;

type BotWithHistory = Bot & { explorationHistory?: { x: number; z: number }[] };

export function evaluateExplore(
    bot: Bot,
    perception: Perception,
    plan: ActionPlan,
): ActionPlan {
    const { intent, state } = perception;
    const botWithHistory = bot as BotWithHistory;
    botWithHistory.explorationHistory = botWithHistory.explorationHistory || [];

    // If we have a current exploration target and haven't reached it, keep going
    if (state.targetX !== undefined && state.targetZ !== undefined) {
        const dist = Math.sqrt(
            (bot.entity.position.x - state.targetX) ** 2 +
            (bot.entity.position.z - state.targetZ) ** 2
        );
        
        if (dist > 2) {
            plan.pathfindingGoal = new goals.GoalNearXZ(state.targetX, state.targetZ, 2);
            return plan;
        }
        
        // Target reached, pick a new one
        botWithHistory.explorationHistory.push({ x: state.targetX, z: state.targetZ });
        while (botWithHistory.explorationHistory.length > MAX_HISTORY_POINTS) {
            botWithHistory.explorationHistory.shift();
        }
        delete state.targetX;
        delete state.targetZ;
    }

    // Pick a new direction
    const dist = 16 + Math.random() * 16;
    let bestTarget = null;
    let maxRepulsionScore = -1;
    const pos = bot.entity.position.floored();

    for (let i = 0; i < 24; i++) {
        const testAngle = Math.random() * Math.PI * 2;
        let tx = pos.x;
        let tz = pos.z;
        let ty = pos.y;
        let validLength = 0;

        for (let step = 1; step <= dist; step++) {
            const checkX = Math.floor(pos.x + Math.cos(testAngle) * step);
            const checkZ = Math.floor(pos.z + Math.sin(testAngle) * step);
            let foundSurface = false;

            for (let yOffset = 2; yOffset >= -3; yOffset--) {
                const block = bot.blockAt(new Vec3(checkX, ty + yOffset, checkZ));
                const blockAbove = bot.blockAt(new Vec3(checkX, ty + yOffset + 1, checkZ));
                const blockAbove2 = bot.blockAt(new Vec3(checkX, ty + yOffset + 2, checkZ));

                if (!block || !blockAbove || !blockAbove2) break;

                if (block.boundingBox === "block" && blockAbove.name === "air" && blockAbove2.name === "air" &&
                    !["water", "lava", "magma_block", "cactus", "sweet_berry_bush"].includes(block.name)) {
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

        if (validLength < 4) continue;

        let minDistanceToHistory = 999999;
        for (const pt of botWithHistory.explorationHistory) {
            const d = Math.sqrt((tx - pt.x) ** 2 + (tz - pt.z) ** 2);
            if (d < minDistanceToHistory) minDistanceToHistory = d;
        }
        if (botWithHistory.explorationHistory.length === 0) minDistanceToHistory = validLength;

        const score = validLength * 0.3 + minDistanceToHistory * 0.7;
        if (score > maxRepulsionScore) {
            maxRepulsionScore = score;
            bestTarget = { x: tx, z: tz };
        }
    }

    if (!bestTarget) {
        const angle = Math.random() * Math.PI * 2;
        const fallbackDist = 8 + Math.random() * 8;
        bestTarget = {
            x: pos.x + Math.cos(angle) * fallbackDist,
            z: pos.z + Math.sin(angle) * fallbackDist,
        };
    }

    state.targetX = bestTarget.x;
    state.targetZ = bestTarget.z;
    
    plan.pathfindingGoal = new goals.GoalNearXZ(state.targetX, state.targetZ, 2);
    return plan;
}
