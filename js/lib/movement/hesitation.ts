// js/lib/movement/hesitation.ts
import type { Bot } from "mineflayer";

/**
 * Applies situational hesitation before movement starts.
 * Synced to server physics ticks to prevent event loop drift.
 */
export async function applyHesitation(bot: Bot): Promise<void> {
    let baseTicks = 3; // base reaction time (~150ms)

    // Risk-Based Hesitation (Caution factor)
    if (bot.health < 20) {
        // ~20ms per lost HP = ~0.4 ticks
        baseTicks += Math.floor((20 - bot.health) * 0.4);
    }

    // Proximity threat check
    const threats = bot.nearestEntity((entity) => {
        return (
            entity.type === "mob" &&
            entity.position.distanceTo(bot.entity.position) < 16
        );
    });

    if (threats) {
        baseTicks += 6; // ~300ms extra hesitation
    }

    // Natural Jitter
    const jitterLimit = baseTicks * 0.5;
    const jitterTicks = Math.floor(Math.random() * jitterLimit);

    const finalTicks = baseTicks + jitterTicks;

    if (finalTicks > 0) {
        await bot.waitForTicks(finalTicks);
    }
}
