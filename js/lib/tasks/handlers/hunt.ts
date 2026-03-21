import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { gotoEntity, attackEntity, findNearestEntity } from "../primitives.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";

interface HuntContext extends StateContext {
    stopMovement: () => void;
}

class LootState implements FSMState {
    name = "LOOTING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const hCtx = ctx as HuntContext;
        const droppedItems = findNearestEntity(hCtx.bot, "item", 8);

        if (droppedItems) {
            try {
                const reached = await gotoEntity(hCtx.bot, droppedItems, 0.5, {
                    signal: hCtx.signal,
                    timeoutMs: 10000,
                    stopMovement: hCtx.stopMovement,
                });

                if (!reached) {
                    hCtx.result = {
                        status: "FAILED",
                        reason: "FAILED_TO_REACH_LOOT",
                    };
                    return null;
                }
            } catch (err: any) {
                if (err.message === "aborted") {
                    hCtx.result = { status: "FAILED", reason: "aborted" };
                    return null;
                }
            }
        }
        hCtx.result = { status: "SUCCESS", reason: "HUNT_COMPLETE" };
        return null;
    }
}

class AttackState implements FSMState {
    name = "ATTACKING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        if (!ctx.targetEntity || !ctx.targetEntity.isValid) {
            return new LootState();
        }

        const dist = ctx.bot.entity.position.distanceTo(
            ctx.targetEntity.position,
        );

        if (dist > 3.2) {
            return new ApproachState();
        }

        const weapon = ctx.bot.inventory
            .items()
            .find((i) => i.name.includes("sword") || i.name.includes("axe"));

        if (weapon) await ctx.bot.equip(weapon, "hand");

        await attackEntity(ctx.bot, ctx.targetEntity);
        await new Promise((resolve) => setTimeout(resolve, 650));

        return this;
    }
}

class ApproachState implements FSMState {
    name = "APPROACHING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const hCtx = ctx as HuntContext;

        if (!hCtx.targetEntity || !hCtx.targetEntity.isValid) {
            hCtx.result = {
                status: "FAILED",
                reason: "TARGET_LOST_OR_DESPAWNED",
            };
            return null;
        }

        try {
            const reached = await gotoEntity(hCtx.bot, hCtx.targetEntity, 2, {
                signal: hCtx.signal,
                timeoutMs: 15000,
                stopMovement: hCtx.stopMovement,
            });

            if (!reached) {
                const dist = hCtx.bot.entity.position.distanceTo(
                    hCtx.targetEntity.position,
                );

                if (dist > 32) {
                    hCtx.result = {
                        status: "FAILED",
                        reason: "TARGET_RAN_TOO_FAR",
                    };
                    return null;
                }
            }
        } catch (err: any) {
            if (err.message === "aborted") {
                hCtx.result = { status: "FAILED", reason: "aborted" };
                return null;
            }
        }

        return new AttackState();
    }
}

export class SearchState implements FSMState {
    name = "SEARCHING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const entity = findNearestEntity(
            ctx.bot,
            ctx.targetName,
            ctx.searchRadius,
        );

        if (!entity) {
            ctx.result = {
                status: "FAILED",
                reason: `NO_${ctx.targetName.toUpperCase()}_FOUND_IN_RADIUS`,
            };
            return null;
        }
        ctx.targetEntity = entity;
        return new ApproachState();
    }
}

export async function handleHunt(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const targetName = decision.target?.name;
    if (!targetName || targetName === "none") {
        throw new Error("missing hunt target");
    }

    const fsmCtx: HuntContext = {
        bot,
        targetName,
        targetEntity: null,
        searchRadius: 32,
        timeoutMs: timeouts.hunt ?? 30000,
        startTime: 0,
        signal,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new SearchState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
