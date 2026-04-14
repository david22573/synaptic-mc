import { type Bot, type Chest, type Dispenser } from "mineflayer";
import { type Block } from "prismarine-block";
import { type Item } from "prismarine-item";
import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";
import { navigateWithFallbacks } from "../../movement/navigator.js";
import { Runtime } from "../../control/runtime.js";
import { TaskAbortError, isAbortError } from "../../errors.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface RetrieveContext extends StateContext {
    targetName: string;
    targetCount: number;
    collectedCount: number;
    checkedChests: Set<string>;
    currentChest: Block | null;
    stopMovement: () => void;
}

class WithdrawState implements FSMState {
    name = "WITHDRAWING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as RetrieveContext;
        let chestWindow: Chest | Dispenser | null = null;
        let syncFailures = 0;

        try {
            if (!sCtx.currentChest) return null;
            chestWindow = await sCtx.bot.openContainer(sCtx.currentChest);
            await new Promise((r) => setTimeout(r, 500));

            const items = chestWindow.containerItems();
            const targetItems = items.filter(
                (i: Item) => i.name === sCtx.targetName,
            );

            for (const item of targetItems) {
                if (sCtx.signal.aborted) throw new TaskAbortError();

                const needed = sCtx.targetCount - sCtx.collectedCount;
                if (needed <= 0) break;

                const amountToTake = Math.min(item.count, needed);

                try {
                    await chestWindow.withdraw(item.type, null, amountToTake);
                    sCtx.collectedCount += amountToTake;

                    await new Promise((r) =>
                        setTimeout(r, 100 + Math.random() * 50),
                    );

                    syncFailures = 0;
                } catch (err: any) {
                    if (isAbortError(err)) throw new TaskAbortError();
                    syncFailures++;
                    if (syncFailures > 3) {
                        throw new Error(
                            `PANIC: Repeated chest sync failures during withdraw. Chest UI state corrupted: ${err.message}`,
                        );
                    }
                }
            }
        } catch (err: any) {
            if (isAbortError(err)) {
                sCtx.result = { status: "FAILED", reason: "aborted" };
                return null;
            }
            sCtx.result = {
                status: "FAILED",
                reason: `RETRIEVE_FAILED: ${err.message}`,
            };
            return null;
        } finally {
            if (chestWindow) {
                try {
                    chestWindow.close();
                } catch (_err) {}
            }
        }

        if (sCtx.collectedCount >= sCtx.targetCount) {
            sCtx.result = { status: "SUCCESS", reason: "RETRIEVE_COMPLETE" };
            return null;
        }

        return new LocateChestState();
    }
}

class ApproachChestState implements FSMState {
    name = "APPROACHING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as RetrieveContext;
        if (!sCtx.currentChest) return null;
        const cPos = sCtx.currentChest.position;

        try {
            await navigateWithFallbacks(
                sCtx.bot,
                new goals.GoalNear(cPos.x, cPos.y, cPos.z, 2),
                {
                    signal: sCtx.signal,
                    timeoutMs: 15000,
                    stopMovement: sCtx.stopMovement,
                },
            );
        } catch (err: any) {
            if (isAbortError(err)) {
                sCtx.result = { status: "FAILED", reason: "aborted" };
            } else {
                return new LocateChestState();
            }
            return null;
        }

        return new WithdrawState();
    }
}

class LocateChestState implements FSMState {
    name = "LOCATING_CHEST";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as RetrieveContext;

        const chests = sCtx.bot.findBlocks({
            matching: sCtx.bot.registry.blocksByName.chest?.id ?? -1,
            maxDistance: 32,
            count: 10,
        });

        let nextChestPos = null;
        for (const pos of chests) {
            const key = `${pos.x},${pos.y},${pos.z}`;
            if (!sCtx.checkedChests.has(key)) {
                nextChestPos = pos;
                sCtx.checkedChests.add(key);
                break;
            }
        }

        if (!nextChestPos) {
            sCtx.result = {
                status: "FAILED",
                reason: `MISSING_INGREDIENTS: Not enough ${sCtx.targetName} found in nearby chests.`,
            };
            return null;
        }

        sCtx.currentChest = sCtx.bot.blockAt(nextChestPos);
        return new ApproachChestState();
    }
}

export async function handleRetrieve(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;

    await escapeTree(bot, signal);

    const targetName = intent.target?.name;
    if (!targetName) {
        throw new Error(
            "MISSING_INGREDIENTS: No target specified for retrieve",
        );
    }

    const fsmCtx: RetrieveContext = {
        bot,
        targetName: targetName,
        targetCount: intent.count || 1,
        collectedCount: 0,
        checkedChests: new Set(),
        currentChest: null,
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.retrieve,
        startTime: 0,
        signal,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new LocateChestState(), fsmCtx);
    const result = await new Runtime(bot).execute(fsm.run(), signal);

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
