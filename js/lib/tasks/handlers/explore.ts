import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal } from "../utils.js";
import { log } from "../../logger.js"; // <-- ADDED
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface ExploreContext extends StateContext {
    targetX: number;
    targetZ: number;
    stopMovement: () => void;
}

class NavigateState implements FSMState {
    name = "NAVIGATING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const eCtx = ctx as ExploreContext;

        try {
            await moveToGoal(
                eCtx.bot,
                new goals.GoalNearXZ(eCtx.targetX, eCtx.targetZ, 2),
                {
                    signal: eCtx.signal,
                    timeoutMs: 15000,
                    stopMovement: eCtx.stopMovement,
                    dynamic: false,
                },
            );

            eCtx.result = { status: "SUCCESS", reason: "EXPLORED_TARGET_AREA" };
        } catch (err: any) {
            const msg = String(err?.message ?? err);

            if (
                eCtx.signal.aborted ||
                msg.includes("goal was changed") ||
                msg.includes("aborted")
            ) {
                eCtx.result = { status: "FAILED", reason: "aborted" };
            } else if (
                msg.includes("timeout") ||
                msg.includes("no_path") ||
                msg.includes("stuck") ||
                msg.includes("pathfinder_timeout")
            ) {
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

        // Early-game: much shorter explores (prevents sluggish wandering)
        const hasWood = eCtx.bot.inventory
            .items()
            .some((i) => i.name.includes("_log"));
        const dist = hasWood
            ? 48 + Math.random() * 48
            : 24 + Math.random() * 24;

        const angle = Math.random() * Math.PI * 2;

        eCtx.targetX = eCtx.bot.entity.position.x + Math.cos(angle) * dist;
        eCtx.targetZ = eCtx.bot.entity.position.z + Math.sin(angle) * dist;

        log.info("Picked exploration vector", {
            dist: Math.round(dist),
            targetX: Math.round(eCtx.targetX),
            targetZ: Math.round(eCtx.targetZ),
            earlyGame: !hasWood,
        });

        return new NavigateState();
    }
}

export async function handleExplore(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
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
        stopMovement,
    };

    const fsm = new StateMachineRunner(new PickDirectionState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
