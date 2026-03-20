import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface ExploreContext extends StateContext {
    targetX: number;
    targetZ: number;
}

class NavigateState implements FSMState {
    name = "NAVIGATING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const eCtx = ctx as ExploreContext;

        try {
            await (eCtx.bot as any).pathfinder.goto(
                new goals.GoalNearXZ(eCtx.targetX, eCtx.targetZ, 2),
                true,
            );
            eCtx.result = { status: "SUCCESS", reason: "EXPLORED_TARGET_AREA" };
        } catch (err: any) {
            const msg = String(err?.message ?? err);
            if (msg.includes("noPath") || msg.includes("No path")) {
                // Hitting a wall/ocean during blind exploration is normal. We still explored.
                eCtx.result = {
                    status: "SUCCESS",
                    reason: "EXPLORED_UNTIL_OBSTACLE",
                };
            } else {
                eCtx.result = {
                    status: "FAILED",
                    reason: `PATHING_ERROR: ${msg}`,
                };
            }
        }

        return null;
    }
}

class PickDirectionState implements FSMState {
    name = "PICKING_DIRECTION";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const eCtx = ctx as ExploreContext;
        const angle = Math.random() * Math.PI * 2;
        const dist = 30 + Math.random() * 20; // 30-50 blocks

        eCtx.targetX = eCtx.bot.entity.position.x + Math.cos(angle) * dist;
        eCtx.targetZ = eCtx.bot.entity.position.z + Math.sin(angle) * dist;

        return new NavigateState();
    }
}

export async function handleExplore(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts } = ctx;
    await escapeTree(bot, signal);

    const fsmCtx: ExploreContext = {
        bot,
        targetName: "explore",
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.explore ?? 20000,
        startTime: 0,
        signal,
        targetX: 0,
        targetZ: 0,
    };

    const fsm = new StateMachineRunner(new PickDirectionState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
