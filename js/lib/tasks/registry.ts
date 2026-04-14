// js/lib/tasks/registry.ts
import type { Bot } from "mineflayer";
import type * as models from "../models.js";
import { ActionPlan, Perception } from "../control/controller.js";
import { handleHunt, evaluateHunt } from "./handlers/hunt.js";
import { handleGather } from "./handlers/gather.js";

export interface TaskContext {
    bot: Bot;
    intent: models.ActionIntent;
    signal: AbortSignal;
    timeouts: Record<string, number>;
    stopMovement: () => void;
    getThreats: () => any[];
    computeSafeRetreat: (
        threats: any[],
        radius?: number,
    ) => { x: number; z: number };
}

export type TaskHandler = (ctx: TaskContext) => Promise<void>;

// Legacy FSM Handlers
export const TASK_REGISTRY: Record<string, TaskHandler> = {
    gather: handleGather,
    hunt: handleHunt,
};

// Continuous Control Evaluators
export type IntentEvaluator = (
    bot: Bot,
    perception: Perception,
    plan: ActionPlan,
) => ActionPlan;
export const INTENT_EVALUATORS: Record<string, IntentEvaluator> = {
    hunt: evaluateHunt,
};
