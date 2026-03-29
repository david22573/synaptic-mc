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
    makeRoomInInventory,
    waitForMs,
    moveToGoal,
} from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface SmeltContext extends StateContext {
    furnaceBlock: any | null;
    isPortable: boolean;
    meatType: number | null;
    meatCount: number;
    fuelType: number | null;
    fuelCount: number;
    stopMovement: () => void;
}

class CleanupState implements FSMState {
    name = "CLEANUP";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as SmeltContext;

        if (sCtx.isPortable && sCtx.furnaceBlock) {
            await makeRoomInInventory(sCtx.bot, 1);

            const pickaxe = sCtx.bot.pathfinder.bestHarvestTool(
                sCtx.furnaceBlock,
            );

            if (pickaxe) await sCtx.bot.equip(pickaxe, "hand");

            try {
                await sCtx.bot.dig(sCtx.furnaceBlock);
                await waitForMs(1000, sCtx.signal);
            } catch (_err) {}
        }

        sCtx.result = { status: "SUCCESS", reason: "SMELTING_COMPLETE" };
        return null;
    }
}

class SmeltingState implements FSMState {
    name = "SMELTING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as SmeltContext;
        let furnaceWindow: any = null;

        try {
            const inputAmount = Math.min(sCtx.meatCount, 64);
            const fuelAmount = Math.min(sCtx.fuelCount, 64);

            furnaceWindow = await sCtx.bot.openFurnace(sCtx.furnaceBlock);
            await furnaceWindow.putFuel(sCtx.fuelType, null, fuelAmount);
            await furnaceWindow.putInput(sCtx.meatType, null, inputAmount);

            let itemsSmelted = 0;
            const maxWaitMs = Math.min(
                15000 * inputAmount,
                sCtx.timeoutMs ?? 30000,
            );
            const pollIntervalMs = 500;
            let elapsedMs = 0;

            while (elapsedMs < maxWaitMs && itemsSmelted < inputAmount) {
                if (sCtx.signal.aborted) throw new Error("aborted");

                const outItem = furnaceWindow.outputItem();
                if (outItem) {
                    await makeRoomInInventory(sCtx.bot, 1);
                    await furnaceWindow.takeOutput();
                    itemsSmelted += outItem.count;
                } else {
                    await waitForMs(pollIntervalMs, sCtx.signal);
                    elapsedMs += pollIntervalMs;
                }
            }

            if (itemsSmelted === 0) {
                throw new Error("FURNACE_TIMEOUT_OR_LAG");
            }
        } catch (err: any) {
            sCtx.result = {
                status: "FAILED",
                reason:
                    err.message === "aborted"
                        ? "aborted"
                        : `SMELT_INTERACTION_FAILED: ${err.message}`,
            };
            return null;
        } finally {
            if (furnaceWindow) {
                try {
                    furnaceWindow.close();
                } catch (_err) {}
            }
        }

        return new CleanupState();
    }
}

class ApproachFurnaceState implements FSMState {
    name = "APPROACHING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as SmeltContext;
        const fPos = sCtx.furnaceBlock.position;

        try {
            await moveToGoal(
                sCtx.bot,
                new goals.GoalNear(fPos.x, fPos.y, fPos.z, 2),
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
                sCtx.result = {
                    status: "FAILED",
                    reason: "FAILED_TO_REACH_FURNACE",
                };
            }
            return null;
        }

        return new SmeltingState();
    }
}

class LocateFurnaceState implements FSMState {
    name = "LOCATING_FURNACE";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as SmeltContext;
        let furnace = findNearestBlockByName(sCtx.bot, "furnace");

        if (!furnace) {
            furnace = await placePortableUtility(sCtx.bot, "furnace");
            if (!furnace) {
                sCtx.result = {
                    status: "FAILED",
                    reason: "NO_FURNACE_AVAILABLE",
                };
                return null;
            }
            sCtx.isPortable = true;
        }

        sCtx.furnaceBlock = furnace;
        return new ApproachFurnaceState();
    }
}

class CheckResourcesState implements FSMState {
    name = "CHECKING_RESOURCES";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as SmeltContext;

        const rawMeat = sCtx.bot.inventory
            .items()
            .find((i: any) =>
                ["beef", "porkchop", "mutton", "chicken", "rabbit"].includes(
                    i.name,
                ),
            );

        const fuel = sCtx.bot.inventory
            .items()
            .find((i: any) =>
                ["coal", "charcoal", "oak_planks"].includes(i.name),
            );

        if (!rawMeat || !fuel) {
            sCtx.result = { status: "FAILED", reason: "MISSING_MEAT_OR_FUEL" };
            return null;
        }

        sCtx.meatType = rawMeat.type;
        sCtx.meatCount = rawMeat.count;
        sCtx.fuelType = fuel.type;
        sCtx.fuelCount = fuel.count;

        return new LocateFurnaceState();
    }
}

export async function handleSmelt(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;

    await escapeTree(bot, signal);

    const fsmCtx: SmeltContext = {
        bot,
        targetName: "furnace",
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.smelt ?? 30000,
        startTime: 0,
        signal,
        furnaceBlock: null,
        isPortable: false,
        meatType: null,
        meatCount: 0,
        fuelType: null,
        fuelCount: 0,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new CheckResourcesState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
