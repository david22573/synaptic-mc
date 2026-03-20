import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { gotoEntity, attackEntity, findNearestEntity } from "../primitives.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";

class LootState implements FSMState {
    name = "LOOTING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const droppedItems = findNearestEntity(ctx.bot, "item", 8);
        if (droppedItems) {
            const reached = await gotoEntity(ctx.bot, droppedItems, 0.5);
            if (!reached) {
                ctx.result = {
                    status: "FAILED",
                    reason: "FAILED_TO_REACH_LOOT",
                };
                return null;
            }
        }
        ctx.result = { status: "SUCCESS", reason: "HUNT_COMPLETE" };
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
        await new Promise((resolve) => setTimeout(resolve, 650)); // Minecraft attack cooldown

        return this;
    }
}

class ApproachState implements FSMState {
    name = "APPROACHING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        if (!ctx.targetEntity || !ctx.targetEntity.isValid) {
            ctx.result = {
                status: "FAILED",
                reason: "TARGET_LOST_OR_DESPAWNED",
            };
            return null;
        }

        const reached = await gotoEntity(ctx.bot, ctx.targetEntity, 2);
        if (!reached) {
            const dist = ctx.bot.entity.position.distanceTo(
                ctx.targetEntity.position,
            );
            if (dist > 32) {
                ctx.result = { status: "FAILED", reason: "TARGET_RAN_TOO_FAR" };
                return null;
            }
            // If it failed but is still close, it might just be stuck on a block. Try approaching again.
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
    const { bot, decision, signal, timeouts } = ctx;
    await escapeTree(bot, signal);

    const targetName = decision.target?.name;
    if (!targetName || targetName === "none") {
        throw new Error("missing hunt target");
    }

    const fsmCtx: StateContext = {
        bot,
        targetName,
        targetEntity: null,
        searchRadius: 32,
        timeoutMs: timeouts.hunt ?? 30000,
        startTime: 0,
        signal,
    };

    const fsm = new StateMachineRunner(new SearchState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        // Pass the granular failure reason directly back up to the LLM
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
