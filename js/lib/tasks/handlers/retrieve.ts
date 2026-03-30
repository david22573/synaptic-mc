import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal } from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface RetrieveContext extends StateContext {
    targetName: string;
    targetCount: number;
    collectedCount: number;
    checkedChests: Set<string>; // Set of "x,y,z" to avoid getting stuck in loops
    currentChest: any | null;
    stopMovement: () => void;
}

class WithdrawState implements FSMState {
    name = "WITHDRAWING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as RetrieveContext;
        let chestWindow: any = null;

        try {
            chestWindow = await sCtx.bot.openContainer(sCtx.currentChest);

            // Allow time for the server window to sync
            await new Promise((r) => setTimeout(r, 500));

            const items = chestWindow.containerItems();
            const targetItems = items.filter(
                (i: any) => i.name === sCtx.targetName,
            );

            for (const item of targetItems) {
                if (sCtx.signal.aborted) throw new Error("aborted");
                const needed = sCtx.targetCount - sCtx.collectedCount;
                if (needed <= 0) break;

                const amountToTake = Math.min(item.count, needed);
                try {
                    await chestWindow.withdraw(item.type, null, amountToTake);
                    sCtx.collectedCount += amountToTake;
                } catch (err) {
                    // Ignore individual stack failures, try the next stack
                }
            }
        } catch (err: any) {
            sCtx.result = {
                status: "FAILED",
                reason:
                    err.message === "aborted"
                        ? "aborted"
                        : `RETRIEVE_FAILED: ${err.message}`,
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

        // We didn't get enough. Mark this chest as checked and look for another.
        return new LocateChestState();
    }
}

class ApproachChestState implements FSMState {
    name = "APPROACHING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as RetrieveContext;
        const cPos = sCtx.currentChest.position;

        try {
            await moveToGoal(
                sCtx.bot,
                new goals.GoalNear(cPos.x, cPos.y, cPos.z, 2),
                {
                    signal: sCtx.signal,
                    timeoutMs: 15000,
                    stopMovement: sCtx.stopMovement,
                    dynamic: false,
                },
            );
        } catch (err: any) {
            if (err.message === "aborted") {
                sCtx.result = { status: "FAILED", reason: "aborted" };
            } else {
                // If we can't path to this chest, skip it and look for another
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

        // Find all chests within 32 blocks
        const chests = sCtx.bot.findBlocks({
            matching: sCtx.bot.registry.blocksByName.chest?.id ?? -1,
            maxDistance: 32,
            count: 10,
        });

        // Filter out chests we've already checked
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
        timeoutMs: timeouts.retrieve ?? 30000,
        startTime: 0,
        signal,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new LocateChestState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
