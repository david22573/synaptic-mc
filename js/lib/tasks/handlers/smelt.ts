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
} from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface SmeltContext extends StateContext {
    furnaceBlock: any | null;
    isPortable: boolean;
    meatType: number | null;
    fuelType: number | null;
}

class CleanupState implements FSMState {
    name = "CLEANUP";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as SmeltContext;

        if (sCtx.isPortable && sCtx.furnaceBlock) {
            await makeRoomInInventory(sCtx.bot, 1);
            const pickaxe = (sCtx.bot as any).pathfinder.bestHarvestTool(
                sCtx.furnaceBlock,
            );
            if (pickaxe) await sCtx.bot.equip(pickaxe, "hand");

            try {
                await sCtx.bot.dig(sCtx.furnaceBlock);
                await waitForMs(1000, sCtx.signal);
            } catch (_err) {
                // Ignore cleanup errors so we don't fail a successful smelt
            }
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
            furnaceWindow = await sCtx.bot.openFurnace(sCtx.furnaceBlock);
            await furnaceWindow.putFuel(sCtx.fuelType, null, 1);
            await furnaceWindow.putInput(sCtx.meatType, null, 1);

            // Wait for smelt cycle. If signal aborts, it throws and jumps to catch block.
            await waitForMs(11000, sCtx.signal);

            await furnaceWindow.takeOutput();
        } catch (err: any) {
            sCtx.result = {
                status: "FAILED",
                reason:
                    err.message === "aborted"
                        ? "aborted"
                        : "SMELT_INTERACTION_FAILED",
            };
            return null; // Bypass cleanup if we failed hard/aborted
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
            await (sCtx.bot as any).pathfinder.goto(
                new goals.GoalNear(fPos.x, fPos.y, fPos.z, 2),
                true,
            );
        } catch (err: any) {
            sCtx.result = {
                status: "FAILED",
                reason: "FAILED_TO_REACH_FURNACE",
            };
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
        sCtx.fuelType = fuel.type;

        return new LocateFurnaceState();
    }
}

export async function handleSmelt(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts } = ctx;
    await escapeTree(bot, signal);

    const fsmCtx: SmeltContext = {
        bot,
        targetName: "furnace",
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.smelt ?? 30000, // Gave this 30s instead of 20s to account for the 11s burn time safely
        startTime: 0,
        signal,
        furnaceBlock: null,
        isPortable: false,
        meatType: null,
        fuelType: null,
    };

    const fsm = new StateMachineRunner(new CheckResourcesState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
