import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import {
    escapeTree,
    findNearestBlockByName,
    placePortableUtility,
} from "../utils.js";
import { navigateWithFallbacks } from "../../movement/navigator.js";
import { Runtime } from "../../control/runtime.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface StoreContext extends StateContext {
    chestBlock: any | null;
    targetItems: { type: number; count: number }[];
    targetCount: number;
    stopMovement: () => void;
}

class DepositState implements FSMState {
    name = "DEPOSITING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as StoreContext;
        let chestWindow: any = null;

        let syncFailures = 0;

        try {
            chestWindow = await sCtx.bot.openContainer(sCtx.chestBlock);
            await new Promise((r) => setTimeout(r, 500));

            for (const item of sCtx.targetItems) {
                if (sCtx.signal.aborted) throw new Error("aborted");

                try {
                    await chestWindow.deposit(item.type, null, item.count);

                    await new Promise((r) =>
                        setTimeout(r, 100 + Math.random() * 50),
                    );

                    syncFailures = 0;
                } catch (err: any) {
                    syncFailures++;

                    if (syncFailures > 3) {
                        throw new Error(
                            `PANIC: Repeated chest sync failures during deposit. Chest UI state corrupted: ${err.message}`,
                        );
                    }
                }
            }
        } catch (err: any) {
            sCtx.result = {
                status: "FAILED",
                reason:
                    err.message === "aborted"
                        ? "aborted"
                        : `STORE_FAILED: ${err.message}`,
            };
            return null;
        } finally {
            if (chestWindow) {
                try {
                    chestWindow.close();
                } catch (_err) {}
            }
        }

        sCtx.result = { status: "SUCCESS", reason: "STORE_COMPLETE" };
        return null;
    }
}

class ApproachChestState implements FSMState {
    name = "APPROACHING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as StoreContext;
        const cPos = sCtx.chestBlock.position;

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
            sCtx.result = {
                status: "FAILED",
                reason: err.message || "FAILED_TO_REACH_CHEST",
            };
            return null;
        }

        return new DepositState();
    }
}

class LocateChestState implements FSMState {
    name = "LOCATING_CHEST";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as StoreContext;
        let chest = findNearestBlockByName(sCtx.bot, "chest");

        if (!chest) {
            chest = await placePortableUtility(sCtx.bot, "chest");
            if (!chest) {
                sCtx.result = {
                    status: "FAILED",
                    reason: "NO_CHEST_AVAILABLE",
                };
                return null;
            }
        }

        sCtx.chestBlock = chest;

        return new ApproachChestState();
    }
}

class CheckItemsState implements FSMState {
    name = "CHECKING_ITEMS";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as StoreContext;
        const targetName = sCtx.targetName;

        const inventory = sCtx.bot.inventory.items();

        if (targetName === "all" || targetName === "dump") {
            const keepItems = new Set([
                "wooden_pickaxe",
                "stone_pickaxe",
                "iron_pickaxe",
                "sword",
                "crafting_table",
                "coal",
            ]);

            sCtx.targetItems = inventory
                .filter(
                    (i: any) =>
                        !keepItems.has(i.name) &&
                        !i.name.includes("pickaxe") &&
                        !i.name.includes("sword"),
                )
                .map((i: any) => ({ type: i.type, count: i.count }));
        } else {
            const items = inventory.filter((i: any) => i.name === targetName);

            if (items.length === 0) {
                sCtx.result = {
                    status: "FAILED",
                    reason: `MISSING_INGREDIENTS: ${targetName} not_in_inventory`,
                };
                return null;
            }

            let remainingToStore =
                sCtx.targetCount > 0
                    ? sCtx.targetCount
                    : items.reduce((sum: number, i: any) => sum + i.count, 0);

            sCtx.targetItems = [];
            for (const item of items) {
                if (remainingToStore <= 0) break;

                const toStoreFromStack = Math.min(item.count, remainingToStore);
                sCtx.targetItems.push({
                    type: item.type,
                    count: toStoreFromStack,
                });
                remainingToStore -= toStoreFromStack;
            }
        }

        if (sCtx.targetItems.length === 0) {
            sCtx.result = { status: "FAILED", reason: "NO_ITEMS_TO_STORE" };
            return null;
        }

        return new LocateChestState();
    }
}

export async function handleStore(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;

    await escapeTree(bot, signal);

    const fsmCtx: StoreContext = {
        bot,
        targetName: intent.target?.name || "",
        targetCount: intent.count || 0,
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.store ?? 20000,
        startTime: 0,
        signal,
        chestBlock: null,
        targetItems: [],
        stopMovement,
    };

    const fsm = new StateMachineRunner(new CheckItemsState(), fsmCtx);
    const result = await new Runtime(bot).execute(fsm.run(), signal);

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
