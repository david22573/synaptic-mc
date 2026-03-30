import { StateMachineRunner, } from "../fsm.js";
import {} from "../registry.js";
import { escapeTree, findNearestBlockByName, placePortableUtility, moveToGoal, } from "../utils.js";
import pkg from "mineflayer-pathfinder";
const { goals } = pkg;
class DepositState {
    name = "DEPOSITING";
    async enter() { }
    async execute(ctx) {
        const sCtx = ctx;
        let chestWindow = null;
        try {
            chestWindow = await sCtx.bot.openContainer(sCtx.chestBlock);
            for (const item of sCtx.targetItems) {
                if (sCtx.signal.aborted)
                    throw new Error("aborted");
                try {
                    await chestWindow.deposit(item.type, null, item.count);
                }
                catch (err) {
                    // Ignore individual deposit failures if the chest is full
                }
            }
        }
        catch (err) {
            sCtx.result = {
                status: "FAILED",
                reason: err.message === "aborted"
                    ? "aborted"
                    : `STORE_FAILED: ${err.message}`,
            };
            return null;
        }
        finally {
            if (chestWindow) {
                try {
                    chestWindow.close();
                }
                catch (_err) { }
            }
        }
        sCtx.result = { status: "SUCCESS", reason: "STORE_COMPLETE" };
        return null;
    }
}
class ApproachChestState {
    name = "APPROACHING";
    async enter() { }
    async execute(ctx) {
        const sCtx = ctx;
        const cPos = sCtx.chestBlock.position;
        try {
            await moveToGoal(sCtx.bot, new goals.GoalNear(cPos.x, cPos.y, cPos.z, 2), {
                signal: sCtx.signal,
                timeoutMs: 15000,
                stopMovement: sCtx.stopMovement,
                dynamic: false,
            });
        }
        catch (err) {
            if (err.message === "aborted") {
                sCtx.result = { status: "FAILED", reason: "aborted" };
            }
            else {
                sCtx.result = {
                    status: "FAILED",
                    reason: "FAILED_TO_REACH_CHEST",
                };
            }
            return null;
        }
        return new DepositState();
    }
}
class LocateChestState {
    name = "LOCATING_CHEST";
    async enter() { }
    async execute(ctx) {
        const sCtx = ctx;
        let chest = findNearestBlockByName(sCtx.bot, "chest");
        if (!chest) {
            chest = await placePortableUtility(sCtx.bot, "chest");
            if (!chest) {
                sCtx.result = {
                    status: "FAILED",
                    reason: "NO_CHEST_AVAILABLE", // Handled by normalizeFailureCause
                };
                return null;
            }
        }
        sCtx.chestBlock = chest;
        return new ApproachChestState();
    }
}
class CheckItemsState {
    name = "CHECKING_ITEMS";
    async enter() { }
    async execute(ctx) {
        const sCtx = ctx;
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
                .filter((i) => !keepItems.has(i.name) &&
                !i.name.includes("pickaxe") &&
                !i.name.includes("sword"))
                .map((i) => ({ type: i.type, count: i.count }));
        }
        else {
            const item = inventory.find((i) => i.name === targetName);
            if (!item) {
                sCtx.result = {
                    status: "FAILED",
                    reason: `MISSING_INGREDIENTS: ${targetName} not_in_inventory`,
                };
                return null;
            }
            sCtx.targetItems = [{ type: item.type, count: item.count }];
        }
        if (sCtx.targetItems.length === 0) {
            sCtx.result = { status: "FAILED", reason: "NO_ITEMS_TO_STORE" };
            return null;
        }
        return new LocateChestState();
    }
}
export async function handleStore(ctx) {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);
    const fsmCtx = {
        bot,
        targetName: intent.target?.name || "",
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
    const result = await fsm.run();
    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
//# sourceMappingURL=store.js.map