import { type TaskContext } from "../registry.js";
import { Runtime } from "../../control/runtime.js";
import {
    findNearestBlockByName,
    placePortableUtility,
    makeRoomInInventory,
    escapeTree,
    moveToGoal,
} from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export async function handleCraft(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, stopMovement } = ctx;
    const runtime = new Runtime(bot);

    const taskLogic = async () => {
        await escapeTree(bot, signal);

        const targetName = intent.target.name;
        const count = intent.count || 1;

        const itemType = bot.registry.itemsByName[targetName];
        if (!itemType) {
            throw new Error(`UNKNOWN_ITEM: ${targetName}`);
        }

        let craftingTable = findNearestBlockByName(bot, "crafting_table");
        let isPortableTable = false;

        let recipes = bot.recipesFor(itemType.id, null, 1, craftingTable);

        if (recipes.length === 0 && !craftingTable) {
            const hasTableInInv = bot.inventory
                .items()
                .some((i) => i.name === "crafting_table");
            if (hasTableInInv) {
                craftingTable = await placePortableUtility(
                    bot,
                    "crafting_table",
                );
                if (craftingTable) {
                    isPortableTable = true;
                    recipes = bot.recipesFor(
                        itemType.id,
                        null,
                        1,
                        craftingTable,
                    );
                }
            }
        }

        if (recipes.length === 0) {
            throw new Error(
                `MISSING_INGREDIENTS_OR_CRAFTING_TABLE_FOR_${targetName.toUpperCase()}`,
            );
        }

        const recipe = recipes[0];

        if (!recipe) return;

        if (recipe.requiresTable && craftingTable) {
            const pos = craftingTable.position;
            try {
                await moveToGoal(
                    bot,
                    new goals.GoalNear(pos.x, pos.y, pos.z, 2),
                    {
                        signal,
                        timeoutMs: 15000,
                        stopMovement,
                        dynamic: false,
                    },
                );
            } catch (err: any) {
                throw err.message === "aborted"
                    ? err
                    : new Error("FAILED_TO_REACH_TABLE");
            }
        }

        try {
            await makeRoomInInventory(bot, 1);
            const tableToUse = recipe.requiresTable ? craftingTable : null;
            await bot.craft(recipe, count, tableToUse);
        } catch (err: any) {
            throw new Error(`CRAFT_ACTION_FAILED: ${err.message}`);
        } finally {
            if (isPortableTable && craftingTable) {
                await makeRoomInInventory(bot, 1);
                const pickaxe = bot.pathfinder.bestHarvestTool(craftingTable);
                if (pickaxe) await bot.equip(pickaxe, "hand");
                try {
                    await bot.dig(craftingTable);
                } catch (_err) {}
            }
        }
    };

    await runtime.execute(taskLogic(), signal);
}
