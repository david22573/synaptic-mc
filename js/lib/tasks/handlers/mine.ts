import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal, waitForMs } from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface MineContext extends StateContext {
    candidatePositions: any[];
    currentIndex: number;
    targetBlock: any;
    stopMovement: () => void;
}

class MineBlockState implements FSMState {
    name = "BREAKING_ORE";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const mCtx = ctx as MineContext;
        const freshBlock = mCtx.bot.blockAt(mCtx.targetBlock.position);

        if (!freshBlock || freshBlock.name === "air") {
            return advanceToNextOre(mCtx, "BLOCK_ALREADY_MINED");
        }

        const pickaxe = mCtx.bot.inventory
            .items()
            .find((i: any) => i.name.includes("pickaxe"));
        if (!pickaxe) {
            mCtx.result = { status: "FAILED", reason: "NO_PICKAXE_EQUIPPED" };
            return null;
        }

        await mCtx.bot.equip(pickaxe, "hand");

        try {
            await mCtx.bot.dig(freshBlock, true); // true forces line-of-sight bypass
            await waitForMs(500, mCtx.signal);
            mCtx.result = { status: "SUCCESS" };
            return null;
        } catch (err: any) {
            return advanceToNextOre(mCtx, `DIG_FAIL: ${err.message}`);
        }
    }
}

class NavigateOreState implements FSMState {
    name = "NAVIGATING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const mCtx = ctx as MineContext;
        const pos = mCtx.candidatePositions[mCtx.currentIndex];
        mCtx.targetBlock = mCtx.bot.blockAt(pos);

        try {
            // Use GoalGetToBlock so the bot stands *next* to the ore, not inside it
            await moveToGoal(
                mCtx.bot,
                new goals.GoalGetToBlock(pos.x, pos.y, pos.z),
                {
                    signal: mCtx.signal,
                    timeoutMs: 15000,
                    stopMovement: mCtx.stopMovement,
                },
            );
            return new MineBlockState();
        } catch (err: any) {
            return advanceToNextOre(mCtx, `PATH_FAIL: ${err.message}`);
        }
    }
}

class SearchOreState implements FSMState {
    name = "SEARCHING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const mCtx = ctx as MineContext;
        const blockId = mCtx.bot.registry.blocksByName[mCtx.targetName]?.id;

        if (blockId === undefined) {
            mCtx.result = {
                status: "FAILED",
                reason: `UNKNOWN_ORE: ${mCtx.targetName}`,
            };
            return null;
        }

        // Expanded search radius since ores can be sparse
        let blocks = mCtx.bot.findBlocks({
            matching: blockId,
            maxDistance: 64,
            count: 128,
        });

        if (blocks.length === 0) {
            mCtx.result = {
                status: "FAILED",
                reason: `NO_${mCtx.targetName.toUpperCase()}_NEARBY`,
            };
            return null;
        }

        const botPos = mCtx.bot.entity.position;
        blocks.sort(
            (a: any, b: any) => a.distanceTo(botPos) - b.distanceTo(botPos),
        );

        mCtx.candidatePositions = blocks.slice(0, 5);
        mCtx.currentIndex = 0;
        return new NavigateOreState();
    }
}

function advanceToNextOre(mCtx: MineContext, reason: string): FSMState | null {
    mCtx.currentIndex++;
    if (mCtx.currentIndex >= mCtx.candidatePositions.length) {
        mCtx.result = { status: "FAILED", reason: `EXHAUSTED: ${reason}` };
        return null;
    }
    return new NavigateOreState();
}

export async function handleMine(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    if (!decision.target?.name || decision.target.name === "none") {
        throw new Error("missing mine target");
    }

    const fsmCtx: MineContext = {
        bot,
        targetName: decision.target.name,
        targetEntity: null,
        searchRadius: 64,
        timeoutMs: timeouts.mine ?? 45000,
        startTime: 0,
        signal,
        candidatePositions: [],
        currentIndex: 0,
        targetBlock: null,
        stopMovement,
    };

    const result = await new StateMachineRunner(
        new SearchOreState(),
        fsmCtx,
    ).run();
    if (result.status === "FAILED") throw new Error(result.reason);
}
