import type { Bot } from "mineflayer";
import type * as models from "../models.js";
export interface TaskContext {
    bot: Bot;
    intent: models.ActionIntent;
    signal: AbortSignal;
    timeouts: Record<string, number>;
    stopMovement: () => void;
    getThreats: () => any[];
    computeSafeRetreat: (threats: any[], radius?: number) => {
        x: number;
        z: number;
    };
}
export type TaskHandler = (ctx: TaskContext) => Promise<void>;
export declare const TASK_REGISTRY: Record<string, TaskHandler>;
//# sourceMappingURL=registry.d.ts.map