import { Bot } from "mineflayer";
import { ActionPlan, Perception } from "../../control/controller.js";

/**
 * Emergency retreat evaluator using raw vector inversion and mechanical controls.
 * Bypasses A* pathfinding to avoid "impossible path" stutters in tight spaces.
 */
export function evaluateRetreat(bot: Bot, perception: Perception, plan: ActionPlan): ActionPlan {
    const threats = perception.threats || [];
    if (threats.length === 0) {
        // No threats detected, return to idle/normal planning
        return plan;
    }

    // Find the absolute closest threat
    const nearest = threats.sort((a, b) => a.distance - b.distance)[0];

    if (!nearest || !nearest.position) {
        return plan;
    }

    // KILL THE A* SPAM. 
    // We are overriding the pathfinder and taking manual control of the motors.
    plan.clearPathfinder = true; 

    // Calculate a vector pointing strictly AWAY from the threat
    const dx = bot.entity.position.x - nearest.position.x;
    const dz = bot.entity.position.z - nearest.position.z;
    const escapeYaw = Math.atan2(dx, dz);

    // Force the bot to face the escape vector (Fire and forget, don't block tick)
    plan.lookAt = null; // Clear precision looking
    bot.look(escapeYaw, 0, true).catch(() => {});

    // Hold down the W, Sprint, and Spacebar keys blindly
    plan.controls.forward = true;
    plan.controls.sprint = true;
    plan.controls.jump = true; // Bunny hop to clear 1-block obstacles naturally

    // Mark the task state as actively running
    perception.state.escaping = true;

    return plan;
}
