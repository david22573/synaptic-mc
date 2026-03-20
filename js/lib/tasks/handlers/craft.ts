import type { TaskContext } from "../registry.js";
import { moveToGoal, escapeTree, waitForMs } from "../task.js";

export async function handleCraft(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const target = decision.target?.name;
    if (!target) throw new Error("missing craft target");

    const itemType = bot.registry.itemsByName[target];
    if (!itemType) throw new Error(`unknown item: ${target}`);

    const craftingTable = bot.findBlock({
        maxDistance: 32,
        matching: (b: any) => b?.name === "crafting_table",
    });

    const recipes = bot.recipesFor(itemType.id, null, 1, craftingTable);
    if (recipes.length === 0) {
        throw new Error(`no recipe for ${target}`);
    }

    const recipe = recipes[0];
    if (!recipe) {
        throw new Error(`no recipe found for ${target}`);
    }
    if (recipe.requiresTable && !craftingTable) {
        throw new Error("requires crafting table");
    }

    await bot.craft(recipe, 1, craftingTable ?? undefined);
}
