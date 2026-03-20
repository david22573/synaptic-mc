import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, placePortableUtility, waitForMs } from "../utils.js";

class PlaceState implements FSMState {
    name = "PLACING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const placedBlock = await placePortableUtility(ctx.bot, ctx.targetName);

        if (!placedBlock) {
            ctx.result = {
                status: "FAILED",
                reason: `NO_VALID_SURFACE_FOR_${ctx.targetName.toUpperCase()}`,
            };
            return null;
        }

        await waitForMs(500, ctx.signal);
        ctx.result = { status: "SUCCESS", reason: "BLOCK_PLACED" };
        return null;
    }
}

class CheckInventoryState implements FSMState {
    name = "CHECKING_INVENTORY";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const item = ctx.bot.inventory
            .items()
            .find((i: any) => i.name === ctx.targetName);

        if (!item) {
            ctx.result = {
                status: "FAILED",
                reason: `MISSING_${ctx.targetName.toUpperCase()}_IN_INVENTORY`,
            };
            return null;
        }

        return new PlaceState();
    }
}

export async function handleBuild(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts } = ctx;
    await escapeTree(bot, signal);

    const targetName = decision.target?.name;
    if (!targetName || targetName === "none") {
        throw new Error("missing build target");
    }

    const fsmCtx: StateContext = {
        bot,
        targetName,
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.build ?? 20000,
        startTime: 0,
        signal,
    };

    const fsm = new StateMachineRunner(new CheckInventoryState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
