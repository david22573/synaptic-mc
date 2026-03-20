import type { TaskContext } from "../registry.js";
import {
    findNearestBlockByName,
    placePortableUtility,
    makeRoomInInventory,
    moveToGoal,
    escapeTree,
    waitForMs,
} from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export async function handleCraft(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const targetRecipeName = decision.target?.name;
    if (!targetRecipeName || targetRecipeName === "none") {
        throw new Error("missing craft target");
    }

    const itemType = bot.registry.itemsByName[targetRecipeName];
    if (!itemType) {
        throw new Error(`unknown item: ${targetRecipeName}`);
    }

    let craftingTable = findNearestBlockByName(bot, "crafting_table");
    let isPortableTable = false;

    // First attempt: Get recipes using whatever grid is currently available (2x2 or nearby table)
    let recipes = bot.recipesFor(itemType.id, null, 1, craftingTable);

    // If no recipe is found, we might need a 3x3 grid but our table is still in our inventory.
    if (recipes.length === 0 && !craftingTable) {
        const hasTableInInv = bot.inventory
            .items()
            .some((i) => i.name === "crafting_table");
        if (hasTableInInv) {
            // Deploy the portable table first so Mineflayer knows we have a 3x3 grid
            craftingTable = await placePortableUtility(bot, "crafting_table");
            if (craftingTable) {
                isPortableTable = true;
                // Re-evaluate recipes now that the table exists in the world
                recipes = bot.recipesFor(itemType.id, null, 1, craftingTable);
            }
        }
    }

    // If it's STILL empty, we legitimately don't have the planks/sticks to make the item.
    if (recipes.length === 0) {
        throw new Error(
            `no valid recipe or missing ingredients for ${targetRecipeName}`,
        );
    }

    const recipe = recipes[0];

    // If the recipe requires a table, navigate to it
    if (recipe!.requiresTable && craftingTable) {
        await moveToGoal(
            bot,
            new goals.GoalNear(
                craftingTable.position.x,
                craftingTable.position.y,
                craftingTable.position.z,
                2,
            ),
            signal,
            timeouts.craft ?? 20000,
            stopMovement,
        );
    }

    if (signal.aborted) throw new Error("aborted");

    await bot.craft(recipe!, 1, craftingTable);

    // Cleanup: Break and pick up the portable table so we don't leave garbage everywhere
    if (isPortableTable && craftingTable) {
        await makeRoomInInventory(bot, 1);
        const pickaxe = (bot as any).pathfinder.bestHarvestTool(craftingTable);
        if (pickaxe) await bot.equip(pickaxe, "hand");
        await bot.dig(craftingTable);
        await waitForMs(1000, signal);
    }
}
