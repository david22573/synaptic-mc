import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal } from "../utils.js";
import { log } from "../../logger.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface ExploreContext extends StateContext {
    targetX: number;
    targetZ: number;
    attempts: number; // Track how many directions we've tried
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

            // If we actually reached the destination without throwing an error
            eCtx.result = { status: "SUCCESS", reason: "EXPLORED_TARGET_AREA" };
            return null;
        } catch (err: any) {
            const msg = String(err?.message ?? err);

            if (eCtx.signal.aborted || msg.includes("aborted")) {
                eCtx.result = { status: "FAILED", reason: "aborted" };
                return null;
            }

            // If pathing failed due to terrain, DO NOT report success yet.
            // Loop back to PickDirectionState to try a new angle!
            log.warn("Exploration path failed, picking new direction", {
                reason: msg,
            });
            return new PickDirectionState();
        }
    }
}

class PickDirectionState implements FSMState {
    name = "PICKING_DIRECTION";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const eCtx = ctx as ExploreContext;

        eCtx.attempts++;
        if (eCtx.attempts > 4) {
            eCtx.result = {
                status: "FAILED",
                reason: "SURROUNDED_BY_OBSTACLES_OR_WATER",
            };
            return null;
        }

        // Shorter, more reliable hops (16 to 32 blocks).
        // A* can calculate this almost instantly, and it keeps chunks loaded.
        const dist = 16 + Math.random() * 16;

        // Pick an angle. If this is a retry, bias it away from the failed angle.
        const baseAngle = eCtx.attempts > 1 ? eCtx.attempts * (Math.PI / 2) : 0;
        const angle = baseAngle + (Math.random() * Math.PI - Math.PI / 2);

        eCtx.targetX = eCtx.bot.entity.position.x + Math.cos(angle) * dist;
        eCtx.targetZ = eCtx.bot.entity.position.z + Math.sin(angle) * dist;

        log.info("Picked exploration vector", {
            dist: Math.round(dist),
            targetX: Math.round(eCtx.targetX),
            targetZ: Math.round(eCtx.targetZ),
            attempt: eCtx.attempts,
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
        timeoutMs: timeouts.explore ?? 25000, // Slightly longer to allow retries
        startTime: 0,
        signal,
        targetX: 0,
        targetZ: 0,
        attempts: 0,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new PickDirectionState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
