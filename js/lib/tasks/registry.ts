// js/lib/tasks/registry.ts
import type { Bot } from "mineflayer";
import type * as models from "../models.js";
import { ActionPlan, Perception } from "../control/controller.js";
import { evaluateHunt } from "./handlers/hunt.js";
import { evaluateGather } from "./handlers/gather.js";
import { evaluateExplore } from "./handlers/explore.js";
import { evaluateRandomWalk } from "./handlers/random_walk.js";
import { evaluateRetreat } from "./handlers/retreat.js";

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
};

// Continuous Control Evaluators
export type IntentEvaluator = (
    bot: Bot,
    perception: Perception,
    plan: ActionPlan,
) => ActionPlan;
export const INTENT_EVALUATORS: Record<string, IntentEvaluator> = {
    hunt: evaluateHunt,
    gather: evaluateGather,
    explore: evaluateExplore,
    random_walk: evaluateRandomWalk,
    retreat: evaluateRetreat,
};
