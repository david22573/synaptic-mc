import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal, waitForMs } from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

const LOG_TYPES = [
    "oak_log",
    "birch_log",
    "spruce_log",
    "acacia_log",
    "jungle_log",
    "dark_oak_log",
    "mangrove_log",
    "cherry_log",
];

interface GatherContext extends StateContext {
    candidatePositions: any[];
    currentIndex: number;
    resolvedTarget: string;
    targetBlock: any;
    stopMovement: () => void;
}

class MineState implements FSMState {
    name = "MINING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const gCtx = ctx as GatherContext;
        const freshBlock = gCtx.bot.blockAt(gCtx.targetBlock.position);

        if (!freshBlock || freshBlock.name !== gCtx.resolvedTarget)
            return advanceToNextCandidate(gCtx, "BLOCK_CHANGED");

        const tool = gCtx.bot.pathfinder.bestHarvestTool(freshBlock);
        if (tool) await gCtx.bot.equip(tool, "hand");

        try {
            await gCtx.bot.dig(freshBlock);

            const blockId =
                gCtx.bot.registry.blocksByName[gCtx.resolvedTarget]?.id;

            if (blockId) {
                const nearby = gCtx.bot.findBlocks({
                    matching: blockId,
                    maxDistance: 4,
                    count: 16,
                });

                for (const pos of nearby) {
                    if (gCtx.signal.aborted) break;
                    const nextBlock = gCtx.bot.blockAt(pos);
                    if (nextBlock && nextBlock.name !== "air") {
                        const nextTool =
                            gCtx.bot.pathfinder.bestHarvestTool(nextBlock);
                        if (nextTool) await gCtx.bot.equip(nextTool, "hand");
                        await gCtx.bot.dig(nextBlock).catch(() => {});
                    }
                }
            }

            await waitForMs(500, gCtx.signal);

            gCtx.result = { status: "SUCCESS" };
            return null;
        } catch (err: any) {
            return advanceToNextCandidate(gCtx, `DIG_FAIL: ${err.message}`);
        }
    }
}

class NavigateState implements FSMState {
    name = "NAVIGATING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const gCtx = ctx as GatherContext;
        const pos = gCtx.candidatePositions[gCtx.currentIndex];

        gCtx.targetBlock = gCtx.bot.blockAt(pos);
        if (!gCtx.targetBlock) {
            return advanceToNextCandidate(gCtx, "BLOCK_UNLOADED");
        }
        gCtx.resolvedTarget = gCtx.targetBlock.name;

        try {
            await moveToGoal(
                gCtx.bot,
                new goals.GoalNear(pos.x, pos.y, pos.z, 1.5),
                {
                    signal: gCtx.signal,
                    timeoutMs: 15000,
                    stopMovement: gCtx.stopMovement,
                },
            );

            return new MineState();
        } catch (err: any) {
            return advanceToNextCandidate(gCtx, `PATH_FAIL: ${err.message}`);
        }
    }
}

class SearchState implements FSMState {
    name = "SEARCHING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const gCtx = ctx as GatherContext;

        const candidates =
            gCtx.targetName === "wood" ? LOG_TYPES : [gCtx.targetName];

        const ids = candidates
            .map((n) => gCtx.bot.registry.blocksByName[n]?.id)
            .filter((id) => id !== undefined);

        let blocks = gCtx.bot.findBlocks({
            matching: ids,
            maxDistance: 32,
            count: 64,
        });

        const botPos = gCtx.bot.entity.position;
        blocks = blocks.filter((b: any) => Math.abs(b.y - botPos.y) < 12);

        if (blocks.length === 0) {
            gCtx.result = { status: "FAILED", reason: "NO_REACHABLE_BLOCKS" };
            return null;
        }

        blocks.sort(
            (a: any, b: any) => a.distanceTo(botPos) - b.distanceTo(botPos),
        );

        gCtx.candidatePositions = blocks.slice(0, 6);
        gCtx.currentIndex = 0;

        return new NavigateState();
    }
}

function advanceToNextCandidate(
    gCtx: GatherContext,
    reason: string,
): FSMState | null {
    gCtx.currentIndex++;

    if (
        gCtx.currentIndex >= gCtx.candidatePositions.length ||
        gCtx.currentIndex > 5
    ) {
        gCtx.result = { status: "FAILED", reason: `EXHAUSTED: ${reason}` };
        return null;
    }
    return new NavigateState();
}

export async function handleGather(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;

    await escapeTree(bot, signal);

    const fsmCtx: GatherContext = {
        bot,
        targetName: decision.target?.name,
        targetEntity: null,
        searchRadius: 32,
        timeoutMs: timeouts.gather ?? 30000,
        startTime: 0,
        signal,
        candidatePositions: [],
        currentIndex: 0,
        resolvedTarget: "",
        targetBlock: null,
        stopMovement,
    };

    const result = await new StateMachineRunner(
        new SearchState(),
        fsmCtx,
    ).run();

    if (result.status === "FAILED") throw new Error(result.reason);
}
