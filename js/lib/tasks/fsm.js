import { getErrorMessage } from "../utils/errors.js";
export class StateMachineRunner {
    currentState = null;
    context;
    constructor(initialState, context) {
        this.currentState = initialState;
        this.context = context;
    }
    async run() {
        this.context.startTime = Date.now();
        if (this.currentState) {
            await this.currentState.enter(this.context);
        }
        while (this.currentState !== null) {
            if (this.context.signal.aborted) {
                return { status: "FAILED", reason: "aborted" };
            }
            if (Date.now() - this.context.startTime > this.context.timeoutMs) {
                return { status: "FAILED", reason: "FSM_GLOBAL_TIMEOUT" };
            }
            try {
                const nextState = await this.currentState.execute(this.context);
                if (nextState !== this.currentState) {
                    if (nextState) {
                        await nextState.enter(this.context);
                    }
                    this.currentState = nextState;
                }
            }
            catch (err) {
                return {
                    status: "FAILED",
                    reason: `FSM_CRASH: ${getErrorMessage(err)}`,
                };
            }
        }
        return (this.context.result || {
            status: "FAILED",
            reason: "UNKNOWN_TERMINATION",
        });
    }
}
//# sourceMappingURL=fsm.js.map