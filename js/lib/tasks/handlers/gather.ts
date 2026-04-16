import type { Bot } from "mineflayer";
import { ActionPlan, Perception } from "../../control/controller.js";
import { LOG_BLOCK_NAMES } from "../utils.js";
import { Block } from "prismarine-block";

function resolveGatherTargets(targetName: string): string[] {
    if (targetName === "log") {
        return [...LOG_BLOCK_NAMES];
    }

    if (
        LOG_BLOCK_NAMES.includes(targetName as (typeof LOG_BLOCK_NAMES)[number])
    ) {
        return [
            targetName,
            ...LOG_BLOCK_NAMES.filter((name) => name !== targetName),
        ];
    }

    return [targetName];
}

export function evaluateGather(
    bot: Bot,
    perception: Perception,
    plan: ActionPlan,
): ActionPlan {
    const { intent, state } = perception;
    const targetName = intent?.target?.name?.toLowerCase();

    if (!targetName) {
        state.failed = true;
        state.reason = "No target specified";
        return plan;
    }

    // Resolve specific block targets
    const candidateNames = resolveGatherTargets(targetName);
    
    // 1. Find nearest matching block
    const block = bot.findBlock({
        matching: (b: Block) => candidateNames.includes(b.name),
        maxDistance: 48,
    });

    if (!block) {
        state.failed = true;
        state.reason = `No ${targetName} found nearby`;
        return plan;
    }

    // 2. Continuous Pathing
    plan.pathfindingGoal = { x: block.position.x, y: block.position.y, z: block.position.z };
    plan.lookAt = block.position.offset(0.5, 0.5, 0.5);

    // 3. Interaction Logic
    const dist = bot.entity.position.distanceTo(block.position);
    if (dist <= 4.5) {
        plan.interact = "use"; // Placeholder for mining/harvesting
        plan.interactTarget = block;
        
        // Basic mining: clear controls
        plan.controls.forward = false;
        plan.controls.sprint = false;
    }

    return plan;
}
