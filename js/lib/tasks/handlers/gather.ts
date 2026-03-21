import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import type { TaskContext } from "../registry.js";
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

        if (!freshBlock || freshBlock.name !== gCtx.resolvedTarget) {
            return advanceToNextCandidate(gCtx, "BLOCK_GONE_OR_CHANGED");
        }

        const tool = (gCtx.bot as any).pathfinder.bestHarvestTool(freshBlock);
        if (tool != null) await gCtx.bot.equip(tool, "hand");

        try {
            await gCtx.bot.dig(freshBlock);
            await waitForMs(500, gCtx.signal);
            gCtx.result = { status: "SUCCESS", reason: "GATHERED_BLOCK" };
            return null;
        } catch (err: any) {
            if (err.message === "aborted") {
                gCtx.result = { status: "FAILED", reason: "aborted" };
                return null;
            }
            return advanceToNextCandidate(gCtx, `DIG_FAILED: ${err.message}`);
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
        gCtx.resolvedTarget = gCtx.targetBlock.name;

        try {
            await moveToGoal(
                gCtx.bot,
                new goals.GoalGetToBlock(pos.x, pos.y, pos.z),
                {
                    signal: gCtx.signal,
                    timeoutMs: 12000,
                    stopMovement: gCtx.stopMovement,
                    dynamic: false,
                },
            );
            return new MineState();
        } catch (err: any) {
            if (err.message === "aborted") {
                gCtx.result = { status: "FAILED", reason: "aborted" };
                return null;
            }
            return advanceToNextCandidate(
                gCtx,
                `PATHING_FAILED: ${err.message}`,
            );
        }
    }
}

class SearchState implements FSMState {
    name = "SEARCHING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const gCtx = ctx as GatherContext;
        const requestedTarget = gCtx.targetName;

        const isLogRequest =
            requestedTarget.endsWith("_log") || requestedTarget === "wood";

        const candidates = isLogRequest
            ? [
                  requestedTarget,
                  ...LOG_TYPES.filter((l) => l !== requestedTarget),
              ]
            : [requestedTarget];

        const candidateIds = candidates
            .map((name) => gCtx.bot.registry.blocksByName[name]?.id)
            .filter((id) => id !== undefined);

        const blockPositions = gCtx.bot.findBlocks({
            matching: candidateIds,
            maxDistance: 48,
            count: 15, // Increase count to bypass canopies effectively
        });

        if (blockPositions.length === 0) {
            gCtx.result = {
                status: "FAILED",
                reason: `NO_${requestedTarget.toUpperCase()}_NEARBY_MUST_EXPLORE`,
            };
            return null;
        }

        const botPos = gCtx.bot.entity.position;

        // Sort by distance but penalize heavily for height differences to favor ground logs
        blockPositions.sort((a: any, b: any) => {
            const distA = a.distanceTo(botPos);
            const distB = b.distanceTo(botPos);
            const heightPenA = Math.abs(a.y - botPos.y) * 3;
            const heightPenB = Math.abs(b.y - botPos.y) * 3;
            return distA + heightPenA - (distB + heightPenB);
        });

        gCtx.candidatePositions = blockPositions;
        gCtx.currentIndex = 0;

        return new NavigateState();
    }
}

function advanceToNextCandidate(
    gCtx: GatherContext,
    failReason: string,
): FSMState | null {
    gCtx.currentIndex++;
    if (gCtx.currentIndex >= gCtx.candidatePositions.length) {
        gCtx.result = {
            status: "FAILED",
            reason: `EXHAUSTED_ALL_CANDIDATES: Last error was ${failReason}`,
        };
        return null;
    }
    return new NavigateState();
}

export async function handleGather(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const targetName = decision.target?.name;
    if (!targetName) throw new Error("missing gather target");

    const fsmCtx: GatherContext = {
        bot,
        targetName,
        targetEntity: null,
        searchRadius: 48,
        timeoutMs: timeouts.gather ?? 30000,
        startTime: 0,
        signal,
        candidatePositions: [],
        currentIndex: 0,
        resolvedTarget: "",
        targetBlock: null,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new SearchState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
