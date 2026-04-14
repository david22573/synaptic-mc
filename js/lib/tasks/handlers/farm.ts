import { type Bot } from "mineflayer";
import { type Block } from "prismarine-block";
import { type Item } from "prismarine-item";
import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, waitForMs } from "../utils.js";
import { Runtime } from "../../control/runtime.js";
import { navigateWithFallbacks } from "../../movement/navigator.js";
import { TaskAbortError, isAbortError } from "../../errors.js";
import { Vec3 } from "vec3";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

const CROP_DATA: Record<string, { matureAge: number; seedName: string }> = {
    wheat: { matureAge: 7, seedName: "wheat_seeds" },
    carrots: { matureAge: 7, seedName: "carrot" },
    potatoes: { matureAge: 7, seedName: "potato" },
    beetroots: { matureAge: 3, seedName: "beetroot_seeds" },
};

interface FarmContext extends StateContext {
    targetCount: number;
    candidatePositions: Vec3[];
    currentIndex: number;
    targetBlock: Block | null;
    stopMovement: () => void;
}

class ReplantState implements FSMState {
    name = "REPLANTING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const fCtx = ctx as FarmContext;
        const cropInfo = CROP_DATA[fCtx.targetName];
        if (!fCtx.targetBlock) return null;
        const farmlandPos = fCtx.targetBlock.position.offset(0, -1, 0);
        const farmlandBlock = fCtx.bot.blockAt(farmlandPos);
        if (farmlandBlock && farmlandBlock.name === "farmland") {
            const seed = fCtx.bot.inventory
                .items()
                .find((i: Item) => i.name === cropInfo!.seedName);
            if (seed) {
                try {
                    await fCtx.bot.equip(seed, "hand");
                    await fCtx.bot.placeBlock(farmlandBlock, new Vec3(0, 1, 0));
                    await waitForMs(300, fCtx.signal);
                } catch (err) {}
            }
        }
        fCtx.result = { status: "SUCCESS", reason: "HARVESTED_AND_REPLANTED" };
        return null;
    }
}

class HarvestState implements FSMState {
    name = "HARVESTING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const fCtx = ctx as FarmContext;
        if (!fCtx.targetBlock) return null;
        const cropBlock = fCtx.bot.blockAt(fCtx.targetBlock.position);
        if (!cropBlock || cropBlock.name !== fCtx.targetName)
            return advanceToNextCrop(fCtx, "CROP_MISSING");
        try {
            await fCtx.bot.dig(cropBlock, true);
            await waitForMs(1000, fCtx.signal);
            return new ReplantState();
        } catch (err: any) {
            if (isAbortError(err)) throw new TaskAbortError();
            return advanceToNextCrop(fCtx, `DIG_FAIL: ${err.message}`);
        }
    }
}

class NavigateCropState implements FSMState {
    name = "NAVIGATING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const fCtx = ctx as FarmContext;
        const pos = fCtx.candidatePositions[fCtx.currentIndex];
        fCtx.targetBlock = fCtx.bot.blockAt(pos);
        try {
            fCtx.bot.clearControlStates();
            await navigateWithFallbacks(
                fCtx.bot,
                new goals.GoalGetToBlock(pos.x, pos.y, pos.z),
                {
                    signal: fCtx.signal,
                    timeoutMs: 12000,
                    stopMovement: fCtx.stopMovement,
                    maxRetries: 2,
                },
            );
            return new HarvestState();
        } catch (err: any) {
            if (isAbortError(err)) throw new TaskAbortError();
            return advanceToNextCrop(fCtx, `PATH_FAIL: ${err.message}`);
        }
    }
}

class SearchCropState implements FSMState {
    name = "SEARCHING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const fCtx = ctx as FarmContext;
        const cropInfo = CROP_DATA[fCtx.targetName];
        if (!cropInfo) {
            fCtx.result = {
                status: "FAILED",
                reason: `UNSUPPORTED_CROP: ${fCtx.targetName}`,
            };
            return null;
        }
        const blockId = fCtx.bot.registry.blocksByName[fCtx.targetName]?.id;
        let blocks = fCtx.bot.findBlocks({
            matching: blockId!,
            maxDistance: 32,
            count: 128,
        });
        const botPos = fCtx.bot.entity.position;
        blocks = blocks.filter((pos: Vec3) => {
            const block = fCtx.bot.blockAt(pos);
            return block && block.metadata === cropInfo.matureAge;
        });
        if (blocks.length === 0) {
            fCtx.result = {
                status: "FAILED",
                reason: `NO_MATURE_${fCtx.targetName.toUpperCase()}_FOUND`,
            };
            return null;
        }
        blocks.sort(
            (a: Vec3, b: Vec3) => a.distanceTo(botPos) - b.distanceTo(botPos),
        );
        fCtx.candidatePositions = blocks.slice(0, fCtx.targetCount);
        fCtx.currentIndex = 0;
        return new NavigateCropState();
    }
}

function advanceToNextCrop(fCtx: FarmContext, reason: string): FSMState | null {
    fCtx.currentIndex++;
    if (fCtx.currentIndex >= fCtx.candidatePositions.length) {
        fCtx.result = { status: "FAILED", reason: `EXHAUSTED: ${reason}` };
        return null;
    }
    return new NavigateCropState();
}

export async function handleFarm(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);
    const fsmCtx: FarmContext = {
        bot,
        targetName: intent.target?.name || "",
        targetCount: intent.count || 10,
        targetEntity: null,
        searchRadius: 32,
        timeoutMs: timeouts.farm ?? 40000,
        startTime: 0,
        signal,
        candidatePositions: [],
        currentIndex: 0,
        targetBlock: null,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new SearchCropState(), fsmCtx);
    const result = await new Runtime(bot).execute(fsm.run(), signal);

    if (result.status === "FAILED") throw new Error(result.reason);
}
