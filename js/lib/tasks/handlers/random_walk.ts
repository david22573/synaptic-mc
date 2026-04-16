import type { Bot } from "mineflayer";
import { ActionPlan, Perception } from "../../control/controller.js";

/**
 * Braindead, zero-pathfinding movement evaluator for emergency recovery.
 * Just picks a direction and runs/jumps blindly to break geometric deadlocks.
 */
export function evaluateRandomWalk(bot: Bot, perception: Perception, plan: ActionPlan): ActionPlan {
    // If we are stuck, just pick a random direction and sprint-jump blindly
    if (!perception.state.panicYaw) {
        perception.state.panicYaw = (Math.random() * Math.PI * 2) - Math.PI;
    }

    plan.lookAt = null; // Don't look at a specific block
    bot.look(perception.state.panicYaw, 0, true).catch(() => {});

    plan.controls.forward = true;
    plan.controls.sprint = true;
    plan.controls.jump = true; // Bunny hop to clear 1-block obstacles
    plan.clearPathfinder = true; // DO NOT USE A*

    return plan;
}
