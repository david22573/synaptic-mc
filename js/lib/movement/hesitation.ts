// js/lib/movement/hesitation.ts
import type { Bot } from "mineflayer";

/**
 * Applies situational hesitation before movement starts.
 * Ported from the Go cognitive layer to prevent blocking the planner.
 */
export async function applyHesitation(bot: Bot): Promise<void> {
    let base = 150; // base reaction time

    // Risk-Based Hesitation (Caution factor)
    if (bot.health < 20) {
        base += (20 - bot.health) * 20;
    }

    // Proximity threat check
    const threats = bot.nearestEntity((entity) => {
        return (
            entity.type === "mob" &&
            entity.position.distanceTo(bot.entity.position) < 16
        );
    });

    if (threats) {
        base += 300;
    }

    // Natural Jitter
    const jitterLimit = base * 0.5;
    const jitter = Math.random() * jitterLimit;

    const finalMs = base + jitter;

    if (finalMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, finalMs));
    }
}
