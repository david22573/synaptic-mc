// js/lib/tasks/handlers/smelt.ts
import { type TaskContext } from "../registry.js";
import pkg from "mineflayer-pathfinder";
import { log } from "../../logger.js";
import { makeRoomInInventory } from "../utils.js"; // or adjust this path to wherever your inventory helpers live

const { goals } = pkg;

const SMELT_RECIPES: Record<string, string[]> = {
    iron_ingot: ["raw_iron", "iron_ore"],
    gold_ingot: ["raw_gold", "gold_ore"],
    copper_ingot: ["raw_copper", "copper_ore"],
    cooked_beef: ["beef"],
    cooked_porkchop: ["porkchop"],
    cooked_mutton: ["mutton"],
    cooked_chicken: ["chicken"],
    cooked_rabbit: ["rabbit"],
    cooked_cod: ["cod"],
    cooked_salmon: ["salmon"],
    glass: ["sand", "red_sand"],
    stone: ["cobblestone"],
    smooth_stone: ["stone"],
    charcoal: [
        "oak_log",
        "birch_log",
        "spruce_log",
        "jungle_log",
        "acacia_log",
        "dark_oak_log",
        "mangrove_log",
        "cherry_log",
    ],
};

const FUEL_TYPES = [
    "lava_bucket",
    "coal_block",
    "coal",
    "charcoal",
    "oak_log",
    "birch_log",
    "spruce_log",
    "oak_planks",
    "birch_planks",
    "spruce_planks",
    "stick",
];

export async function handleSmelt(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, stopMovement } = ctx;
    const targetName = intent.target.name.toLowerCase();
    const targetCount = intent.count || 1;

    const acceptedInputs = SMELT_RECIPES[targetName];
    if (!acceptedInputs) {
        throw new Error(
            `UNKNOWN_RECIPE: Don't know how to smelt into ${targetName}`,
        );
    }

    const inputItem = bot.inventory
        .items()
        .find((item) => acceptedInputs.includes(item.name));
    if (!inputItem || inputItem.count < targetCount) {
        throw new Error(
            `MISSING_INGREDIENTS: Need at least ${targetCount} of ${acceptedInputs.join(" or ")}`,
        );
    }

    const fuelItem = bot.inventory
        .items()
        .find((item) => FUEL_TYPES.includes(item.name));
    if (!fuelItem) {
        throw new Error(
            "MISSING_INGREDIENTS: No valid fuel source in inventory",
        );
    }

    let furnaceBlock = bot.findBlock({
        matching: bot.registry.blocksByName.furnace?.id ?? -1,
        maxDistance: 16,
    });

    let placedFurnace = false;

    if (!furnaceBlock) {
        const furnaceInInv = bot.inventory
            .items()
            .find((item) => item.name === "furnace");
        if (!furnaceInInv) {
            throw new Error(
                "NO_FURNACE: No furnace nearby and none in inventory",
            );
        }

        const refBlock = bot.findBlock({
            matching: (b) =>
                b.name !== "air" && b.name !== "water" && b.name !== "lava",
            maxDistance: 4,
        });

        if (!refBlock) {
            throw new Error("PATH_FAILED: Nowhere to place the furnace");
        }

        await bot.equip(furnaceInInv.type, "hand");
        try {
            await bot.placeBlock(
                refBlock,
                bot.entity.position.offset(0, 1, 0).minus(refBlock.position),
            );
            placedFurnace = true;
        } catch (err: any) {
            throw new Error(
                `PATH_FAILED: Could not place furnace - ${err.message}`,
            );
        }

        furnaceBlock = bot.findBlock({
            matching: bot.registry.blocksByName.furnace?.id ?? -1,
            maxDistance: 6,
        });
    }

    if (!furnaceBlock) {
        throw new Error("NO_FURNACE: Lost track of furnace after placing it");
    }

    if (bot.entity.position.distanceTo(furnaceBlock.position) > 3) {
        bot.pathfinder.setGoal(
            new goals.GoalBlock(
                furnaceBlock.position.x,
                furnaceBlock.position.y,
                furnaceBlock.position.z,
            ),
            true,
        );

        await new Promise<void>((resolve, reject) => {
            const onGoal = () => {
                cleanup();
                resolve();
            };
            const onStop = (r: any) => {
                if (r.status === "noPath") {
                    cleanup();
                    reject(new Error("PATH_FAILED: Navigation interrupted"));
                }
            };
            const onAbort = () => {
                cleanup();
                reject(new Error("TIMEOUT"));
            };

            const cleanup = () => {
                bot.removeListener("goal_reached", onGoal);
                bot.removeListener("path_update", onStop);
                signal.removeEventListener("abort", onAbort);
                stopMovement();
            };

            bot.on("goal_reached", onGoal);
            bot.on("path_update", onStop);
            signal.addEventListener("abort", onAbort);
        });
    }

    const furnace = await bot.openFurnace(furnaceBlock);
    let collectedCount = 0;

    try {
        while (collectedCount < targetCount) {
            if (signal.aborted) throw new Error("TIMEOUT");

            if (
                !furnace.fuelItem() &&
                (!furnace.fuel || Math.round(furnace.fuel) === 0)
            ) {
                const currentFuel = bot.inventory
                    .items()
                    .find((item) => FUEL_TYPES.includes(item.name));
                if (!currentFuel)
                    throw new Error(
                        "MISSING_INGREDIENTS: Ran out of fuel during smelting",
                    );

                await furnace.putFuel(currentFuel.type, null, 1);
            }

            if (!furnace.inputItem()) {
                const currentInput = bot.inventory
                    .items()
                    .find((item) => acceptedInputs.includes(item.name));
                if (!currentInput)
                    throw new Error(
                        "MISSING_INGREDIENTS: Ran out of raw materials during smelting",
                    );

                const amountToPut = Math.min(
                    currentInput.count,
                    targetCount - collectedCount,
                );
                await furnace.putInput(currentInput.type, null, amountToPut);
            }

            const output = furnace.outputItem();
            if (output && output.name === targetName) {
                const taken = await furnace.takeOutput();
                if (taken) {
                    collectedCount += taken.count;
                    log.info(
                        `Smelted ${taken.count} ${targetName}. Total: ${collectedCount}/${targetCount}`,
                    );
                }
            }

            await new Promise((r) => setTimeout(r, 1000));
        }
    } finally {
        furnace.close();

        if (placedFurnace && !signal.aborted) {
            const pickType =
                bot.registry.itemsByName.wooden_pickaxe?.id ||
                bot.registry.itemsByName.stone_pickaxe?.id;
            if (pickType) {
                await bot.equip(pickType, "hand");
            } else {
                await bot.unequip("hand");
            }
            try {
                await makeRoomInInventory(bot, 1);
                await bot.dig(furnaceBlock);
            } catch (e) {
                log.warn("Failed to retrieve placed furnace", { err: e });
            }
        }
    }
}
