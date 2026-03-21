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

export function normalizeDecision(
    bot: Bot,
    decision: models.IncomingDecision,
): models.IncomingDecision {
    // Clone to avoid mutating the original payload state
    const normalized: models.IncomingDecision = {
        ...decision,
        target: { ...decision.target },
    };

    const action = normalized.action;
    const targetName = (normalized.target?.name || "none").toLowerCase();

    let changed = false;

    if (action === "craft") {
        if (targetName === "planks") {
            // Determine the correct plank type based on available logs
            const invItems = bot.inventory.items();
            let selectedPlank = "";

            for (const item of invItems) {
                if (LOG_TO_PLANK_MAP[item.name]) {
                    selectedPlank = LOG_TO_PLANK_MAP[item.name]!;
                    break;
                }
            }

            if (selectedPlank) {
                normalized.target.name = selectedPlank;
                changed = true;
            } else {
                // Fail semantically early rather than sending the bot to a crafting table for nothing
                throw new Error("MISSING_LOGS_FOR_PLANKS");
            }
        } else if (targetName === "table" || targetName === "workbench") {
            normalized.target.name = "crafting_table";
            changed = true;
        }
    } else if (action === "build") {
        if (targetName === "table" || targetName === "workbench") {
            normalized.target.name = "crafting_table";
            changed = true;
        }
    }

    if (changed) {
        log.debug("Decision normalized", {
            original_target: decision.target.name,
            normalized_target: normalized.target.name,
            action: action,
        });
    }

    return normalized;
}
