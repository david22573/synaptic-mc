import type { Bot } from "mineflayer";
import type { Entity } from "prismarine-entity";
export type TaskStatus = "RUNNING" | "SUCCESS" | "FAILED";
export interface TaskResult {
    status: TaskStatus;
    reason?: string;
    data?: any;
}
export interface StateContext {
    bot: Bot;
    targetName: string;
    targetEntity: Entity | null;
    searchRadius: number;
    timeoutMs: number;
    startTime: number;
    signal: AbortSignal;
    result?: TaskResult;
}
export interface FSMState {
    name: string;
    enter(ctx: StateContext): Promise<void> | void;
    execute(ctx: StateContext): Promise<FSMState | null>;
}
export declare class StateMachineRunner {
    private currentState;
    private context;
    constructor(initialState: FSMState, context: StateContext);
    run(): Promise<TaskResult>;
}
//# sourceMappingURL=fsm.d.ts.map