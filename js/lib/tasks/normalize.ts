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
    const normalized: models.IncomingDecision = {
        ...decision,
        target: { ...decision.target },
    };

    const action = normalized.action;
    let targetName = (normalized.target?.name || "none").toLowerCase().trim();

    let changed = false;

    // ── Craft normalization (most important for progression) ──
    if (action === "craft") {
        // sticks (LLM often says "sticks" plural)
        if (targetName === "sticks" || targetName === "stick") {
            normalized.target.name = "stick";
            changed = true;
        }
        // planks (already good, but now more robust)
        else if (targetName === "planks") {
            const invItems = bot.inventory.items();
            for (const item of invItems) {
                if (LOG_TO_PLANK_MAP[item.name]) {
                    normalized.target.name = LOG_TO_PLANK_MAP[item.name]!;
                    changed = true;
                    break;
                }
            }
            if (!changed) throw new Error("MISSING_LOGS_FOR_PLANKS");
        }
        // table / workbench
        else if (
            targetName === "table" ||
            targetName === "workbench" ||
            targetName === "crafting_table"
        ) {
            normalized.target.name = "crafting_table";
            changed = true;
        }
        // wooden_pickaxe (LLM sometimes says "wooden pickaxe" or "pickaxe")
        else if (
            targetName.includes("wooden_pickaxe") ||
            targetName === "pickaxe"
        ) {
            normalized.target.name = "wooden_pickaxe";
            changed = true;
        }
        // stone_pickaxe (for later phases)
        else if (targetName.includes("stone_pickaxe")) {
            normalized.target.name = "stone_pickaxe";
            changed = true;
        }
    }

    // ── Build normalization ──
    else if (action === "build") {
        if (targetName === "table" || targetName === "workbench") {
            normalized.target.name = "crafting_table";
            changed = true;
        }
    }

    // ── Gather normalization (make "wood" more reliable) ──
    else if (action === "gather" && targetName === "wood") {
        // Keep as-is — the gather handler already expands it to LOG_TYPES
        // but we can force the first available log if we want
    }

    if (changed) {
        log.debug("Decision normalized", {
            original_target: decision.target.name,
            normalized_target: normalized.target.name,
            action,
        });
    }

    return normalized;
}
