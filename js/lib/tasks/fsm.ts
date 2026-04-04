import type { Bot } from "mineflayer";
import type { Entity } from "prismarine-entity";
import { getErrorMessage } from "../utils/errors.js";

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

export class StateMachineRunner {
    private currentState: FSMState | null = null;
    private context: StateContext;

    constructor(initialState: FSMState, context: StateContext) {
        this.currentState = initialState;
        this.context = context;
    }

    public async run(): Promise<TaskResult> {
        this.context.startTime = Date.now();

        if (this.currentState) {
            await this.currentState.enter(this.context);
        }

        while (this.currentState !== null) {
            if (this.context.signal.aborted) {
                return { status: "FAILED", reason: "aborted" };
            }

            const elapsed = Date.now() - this.context.startTime;
            const remaining = this.context.timeoutMs - elapsed;

            if (remaining <= 0) {
                return { status: "FAILED", reason: "FSM_GLOBAL_TIMEOUT" };
            }

            let timeoutId: NodeJS.Timeout | undefined;
            const timeoutPromise = new Promise<never>((_, reject) => {
                timeoutId = setTimeout(() => {
                    reject(new Error("FSM_GLOBAL_TIMEOUT"));
                }, remaining);
            });

            try {
                const nextState = await Promise.race([
                    this.currentState.execute(this.context),
                    timeoutPromise,
                ]);

                if (timeoutId) clearTimeout(timeoutId);

                if (nextState !== this.currentState) {
                    if (nextState) {
                        await nextState.enter(this.context);
                    }
                    this.currentState = nextState;
                }
            } catch (err: unknown) {
                if (timeoutId) clearTimeout(timeoutId);
                const msg = getErrorMessage(err);

                if (msg === "FSM_GLOBAL_TIMEOUT") {
                    return { status: "FAILED", reason: "FSM_GLOBAL_TIMEOUT" };
                }

                if (msg === "aborted" || this.context.signal.aborted) {
                    return { status: "FAILED", reason: "aborted" };
                }

                return {
                    status: "FAILED",
                    reason: `FSM_CRASH: ${msg}`,
                };
            }
        }

        return (
            this.context.result || {
                status: "FAILED",
                reason: "UNKNOWN_TERMINATION",
            }
        );
    }
}
