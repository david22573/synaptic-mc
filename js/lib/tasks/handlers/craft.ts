// js/lib/tasks/handlers/craft.ts
import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import type { TaskContext } from "../registry.js";
import {
    findNearestBlockByName,
    placePortableUtility,
    makeRoomInInventory,
    escapeTree,
    waitForMs,
    moveToGoal,
} from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

interface CraftContext extends StateContext {
    itemType: any;
    recipe: any;
    craftingTable: any;
    isPortableTable: boolean;
    stopMovement: () => void;
}

class CleanupState implements FSMState {
    name = "CLEANUP";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const cCtx = ctx as CraftContext;
        if (cCtx.isPortableTable && cCtx.craftingTable) {
            await makeRoomInInventory(cCtx.bot, 1);
            const pickaxe = cCtx.bot.pathfinder.bestHarvestTool(
                cCtx.craftingTable,
            );
            if (pickaxe) await cCtx.bot.equip(pickaxe, "hand");

            try {
                await cCtx.bot.dig(cCtx.craftingTable);
                await waitForMs(1000, cCtx.signal);
            } catch (_err) {}
        }

        cCtx.result = { status: "SUCCESS", reason: "CRAFTING_COMPLETE" };
        return null;
    }
}

class CraftingState implements FSMState {
    name = "CRAFTING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const cCtx = ctx as CraftContext;
        try {
            // FIX: Prevent bot.craft from hanging indefinitely when inventory is full
            await makeRoomInInventory(cCtx.bot, 1);

            const tableToUse = cCtx.recipe.requiresTable
                ? cCtx.craftingTable
                : null;
            await cCtx.bot.craft(cCtx.recipe, 1, tableToUse);
        } catch (err: any) {
            cCtx.result = {
                status: "FAILED",
                reason: `CRAFT_ACTION_FAILED: ${err.message}`,
            };
            return null;
        }

        return new CleanupState();
    }
}

class NavigateTableState implements FSMState {
    name = "NAVIGATING";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const cCtx = ctx as CraftContext;
        if (cCtx.recipe.requiresTable && cCtx.craftingTable) {
            const pos = cCtx.craftingTable.position;
            try {
                await moveToGoal(
                    cCtx.bot,
                    new goals.GoalNear(pos.x, pos.y, pos.z, 2),
                    {
                        signal: cCtx.signal,
                        timeoutMs: 15000,
                        stopMovement: cCtx.stopMovement,
                        dynamic: false,
                    },
                );
            } catch (err: any) {
                if (err.message === "aborted") {
                    cCtx.result = { status: "FAILED", reason: "aborted" };
                } else {
                    cCtx.result = {
                        status: "FAILED",
                        reason: "FAILED_TO_REACH_TABLE",
                    };
                }
                return null;
            }
        }

        return new CraftingState();
    }
}

class SetupRecipeState implements FSMState {
    name = "SETUP_RECIPE";
    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const cCtx = ctx as CraftContext;
        const targetRecipeName = cCtx.targetName;
        const itemType = cCtx.bot.registry.itemsByName[targetRecipeName];
        if (!itemType) {
            cCtx.result = {
                status: "FAILED",
                reason: `UNKNOWN_ITEM: ${targetRecipeName}`,
            };
            return null;
        }
        cCtx.itemType = itemType;

        let craftingTable = findNearestBlockByName(cCtx.bot, "crafting_table");
        let isPortableTable = false;

        let recipes = cCtx.bot.recipesFor(itemType.id, null, 1, craftingTable);
        if (recipes.length === 0 && !craftingTable) {
            const hasTableInInv = cCtx.bot.inventory
                .items()
                .some((i: any) => i.name === "crafting_table");
            if (hasTableInInv) {
                craftingTable = await placePortableUtility(
                    cCtx.bot,
                    "crafting_table",
                );
                if (craftingTable) {
                    isPortableTable = true;
                    recipes = cCtx.bot.recipesFor(
                        itemType.id,
                        null,
                        1,
                        craftingTable,
                    );
                }
            }
        }

        if (recipes.length === 0) {
            cCtx.result = {
                status: "FAILED",
                reason: `MISSING_INGREDIENTS_OR_CRAFTING_TABLE_FOR_${targetRecipeName.toUpperCase()}`,
            };
            return null;
        }

        cCtx.recipe = recipes[0];
        cCtx.craftingTable = craftingTable;
        cCtx.isPortableTable = isPortableTable;
        return new NavigateTableState();
    }
}

export async function handleCraft(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const targetName = decision.target?.name;
    if (!targetName || targetName === "none") {
        throw new Error("missing craft target");
    }

    const fsmCtx: CraftContext = {
        bot,
        targetName,
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.craft ?? 20000,
        startTime: 0,
        signal,
        itemType: null,
        recipe: null,
        craftingTable: null,
        isPortableTable: false,
        stopMovement,
    };
    const fsm = new StateMachineRunner(new SetupRecipeState(), fsmCtx);
    const result = await fsm.run();
    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
