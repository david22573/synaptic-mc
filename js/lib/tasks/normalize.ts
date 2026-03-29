import type { Bot } from "mineflayer";
import * as models from "../models.js";
import { log } from "../logger.js";

const LOG_TO_PLANK_MAP: Record<string, string> = {
    oak_log: "oak_planks",
    birch_log: "birch_planks",
    spruce_log: "spruce_planks",
    jungle_log: "jungle_planks",
    acacia_log: "acacia_planks",
    dark_oak_log: "dark_oak_planks",
    mangrove_log: "mangrove_planks",
    cherry_log: "cherry_planks",
};

const TARGET_ALIASES: Record<string, string> = {
    // Farming Aliases
    carrot: "carrots",
    potato: "potatoes",
    beetroot: "beetroots",

    // Mining Aliases
    iron: "iron_ore",
    gold: "gold_ore",
    diamond: "diamond_ore",
    coal: "coal_ore",
    copper: "copper_ore",
    lapis: "lapis_ore",
    redstone: "redstone_ore",
    emerald: "emerald_ore",
    cobble: "cobblestone",

    // Gathering Aliases
    tree: "wood",
    log: "wood",
    timber: "wood",
};

export function normalizeIntent(
    bot: Bot,
    intent: models.ActionIntent,
): models.ActionIntent {
    const normalized: models.ActionIntent = {
        ...intent,
        target: { ...intent.target },
    };

    const action = normalized.action;
    const originalTargetName = (normalized.target?.name || "none")
        .toLowerCase()
        .trim();

    let targetName = originalTargetName;

    // 1. Apply static dictionary aliases first
    if (TARGET_ALIASES[targetName]) {
        targetName = TARGET_ALIASES[targetName]!;
    }

    // 2. Apply action-specific dynamic normalization
    if (action === "craft") {
        if (targetName === "sticks" || targetName === "stick") {
            targetName = "stick";
        } else if (targetName === "planks") {
            const invItems = bot.inventory.items();
            let foundLog = false;
            for (const item of invItems) {
                if (LOG_TO_PLANK_MAP[item.name]) {
                    targetName = LOG_TO_PLANK_MAP[item.name]!;
                    foundLog = true;
                    break;
                }
            }
            if (!foundLog) throw new Error("MISSING_LOGS_FOR_PLANKS");
        } else if (
            targetName === "table" ||
            targetName === "workbench" ||
            targetName === "crafting_table"
        ) {
            targetName = "crafting_table";
        } else if (
            targetName.includes("wooden_pickaxe") ||
            targetName === "pickaxe"
        ) {
            targetName = "wooden_pickaxe";
        } else if (targetName.includes("stone_pickaxe")) {
            targetName = "stone_pickaxe";
        }
    } else if (action === "build") {
        if (targetName === "table" || targetName === "workbench") {
            targetName = "crafting_table";
        }
    } else if (action === "gather" && targetName === "wood") {
        // Handled naturally by the gather handler
    }

    // 3. Log and apply changes if a normalization occurred
    if (targetName !== originalTargetName) {
        normalized.target.name = targetName;
        log.debug("Intent normalized", {
            original_target: originalTargetName,
            normalized_target: targetName,
            action,
        });
    }

    return normalized;
}
