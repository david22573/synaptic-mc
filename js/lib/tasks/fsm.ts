import type { Bot } from "mineflayer";
import type { Entity } from "prismarine-entity";
import { getErrorMessage } from "../utils/errors.js";
import { Vec3 } from "vec3";
import * as vm from "node:vm";

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

/**
 * DynamicSkillSandbox acts as a dynamic, secure sandbox that evaluates and executes 
 * arbitrary JavaScript functions injected at runtime from the Go backend.
 * 
 * This implements Requirement 2 for the Voyager-class architecture transition.
 */
export class DynamicSkillSandbox {
    public static async execute(
        code: string,
        context: StateContext
    ): Promise<TaskResult> {
        context.startTime = Date.now();

        // Define secure sandbox globals
        // We provide the bot, utilities, and task-specific context
        const sandbox = {
            bot: context.bot,
            Vec3: Vec3,
            console: console,
            setTimeout: setTimeout,
            clearTimeout: clearTimeout,
            setInterval: setInterval,
            clearInterval: clearInterval,
            Promise: Promise,
            Math: Math,
            Date: Date,
            // Context variables
            targetName: context.targetName,
            targetEntity: context.targetEntity,
            searchRadius: context.searchRadius,
            signal: context.signal,
            // Result object for the dynamic skill to populate
            result: { status: "SUCCESS", reason: "" } as TaskResult,
        };

        const vmContext = vm.createContext(sandbox);

        // We expect 'code' to be a string representation of an async function,
        // e.g., "async (bot, Vec3, ctx) => { ... }"
        // We wrap it to ensure it is evaluated and then executed within the sandbox.
        const wrappedCode = `
            (async () => {
                try {
                    const skillFunc = ${code};
                    await skillFunc(bot, Vec3, { 
                        targetName, 
                        targetEntity, 
                        searchRadius, 
                        signal, 
                        result 
                    });
                } catch (err) {
                    result.status = "FAILED";
                    result.reason = err.message;
                }
            })()
        `;

        try {
            const script = new vm.Script(wrappedCode, {
                filename: "voyager_skill.js",
            });

            // Wall Clock Timeout (Asynchronous)
            // We race the script's internal async execution (which populates context.result)
            // against a timer and the AbortSignal.
            const timeoutPromise = new Promise<TaskResult>((_, reject) => {
                const timer = setTimeout(() => {
                    reject(new Error("SKILL_ASYNC_TIMEOUT"));
                }, context.timeoutMs);
                
                context.signal.addEventListener("abort", () => {
                    clearTimeout(timer);
                    reject(new Error("aborted"));
                }, { once: true });
            });

            // The wrappedCode is an IIFE that returns a promise (via async ())
            // However, script.runInContext returns the result of the last statement,
            // which in our case is the Promise from the IIFE.
            // 
            // timeout: Limits synchronous execution time (CPU Timeout).
            const scriptResult = script.runInContext(vmContext, {
                timeout: context.timeoutMs,
                displayErrors: true,
                breakOnSigint: true,
            }) as Promise<void>;

            await Promise.race([
                scriptResult,
                timeoutPromise
            ]);

            return sandbox.result;
        } catch (err: unknown) {
            const msg = getErrorMessage(err);
            if (context.signal.aborted || msg === "aborted") {
                return { status: "FAILED", reason: "aborted" };
            }
            if (msg === "SKILL_ASYNC_TIMEOUT") {
                return { status: "FAILED", reason: "SKILL_ASYNC_TIMEOUT" };
            }
            return {
                status: "FAILED",
                reason: `SANDBOX_CRASH: ${msg}`,
            };
        }
    }
}

export interface FSMState {
    name: string;
    enter(ctx: StateContext): Promise<void> | void;
    execute(ctx: StateContext): Promise<FSMState | null>;
}

/**
 * StateMachineRunner maintained for legacy FSM support while transitioning 
 * to dynamic skill generation.
 */
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
            try {
                await this.currentState.enter(this.context);
            } catch (err) {
                return { status: "FAILED", reason: `FSM_ENTER_CRASH: ${getErrorMessage(err)}` };
            }
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
                const prevState = this.currentState;
                const nextState = await Promise.race([
                    this.currentState.execute(this.context),
                    timeoutPromise,
                ]);

                if (timeoutId) clearTimeout(timeoutId);

                if (nextState !== prevState) {
                    if (nextState) {
                        await nextState.enter(this.context);
                    }
                    this.currentState = nextState;
                } else {
                    // Yield to event loop to prevent CPU pinning
                    await new Promise(resolve => setImmediate(resolve));
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
