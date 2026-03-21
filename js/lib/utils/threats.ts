import { type Bot } from "mineflayer";
import * as config from "../config.js";
import * as models from "../models.js";

// Optimized O(1) lookup Set - much faster than array includes or generic type checks
const HOSTILE_MOBS = new Set([
    "zombie",
    "zombie_villager",
    "husk",
    "drowned",
    "skeleton",
    "stray",
    "wither_skeleton",
    "creeper",
    "spider",
    "cave_spider",
    "slime",
    "magma_cube",
    "phantom",
    "blaze",
    "ghast",
    "enderman",
    "silverfish",
    "witch",
    "ravager",
    "pillager",
    "evoker",
    "vindicator",
]);

export function getThreats(bot: Bot): models.ThreatInfo[] {
    const threats: models.ThreatInfo[] = [];
    const botPos = bot.entity.position;

    // Use a for...in loop instead of Object.values() to prevent massive memory
    // allocations every tick, which causes garbage collection lag spikes.
    for (const id in bot.entities) {
        const e = bot.entities[id];

        // 1. Skip invalid/dead entities to prevent "ghost mob" tracking bugs
        if (!e || !e.isValid || e === bot.entity) continue;

        // 2. Ensure it's actually a hostile mob
        if (e.type !== "mob" || !HOSTILE_MOBS.has(e.name!)) continue;

        // 3. Check distance (skip math if it's too far away)
        const distance = botPos.distanceTo(e.position);
        if (distance > 16) continue;

        // 4. Calculate your custom threat score
        const baseThreat =
            config.THREAT_WEIGHTS[e.name?.toLowerCase() || ""] || 5;
        const threatScore = baseThreat * (10 / Math.max(distance, 1));

        threats.push({
            id: e.id,
            name: e.name || "unknown",
            distance: parseFloat(distance.toFixed(1)),
            threatScore: Math.round(threatScore),
            position: { x: e.position.x, y: e.position.y, z: e.position.z },
            entity: e, // Kept the entity reference for your other handlers!
        });
    }

    // Sort by highest threat score first (your original logic)
    return threats.sort((a, b) => b.threatScore! - a.threatScore!);
}

// Kept EXACTLY as you wrote it. This weighted vector math is perfect!
export function computeSafeRetreat(
    bot: Bot,
    threats: models.ThreatInfo[],
    distance: number = 20,
) {
    let cx = 0,
        cz = 0,
        totalWeight = 0;

    for (const threat of threats) {
        // Use the non-null assertion (!) if TypeScript complains about threatScore being optional
        cx += threat.position.x * threat.threatScore!;
        cz += threat.position.z * threat.threatScore!;
        totalWeight += threat.threatScore!;
    }

    if (totalWeight === 0) {
        return {
            x: bot.entity.position.x + (Math.random() - 0.5) * distance,
            z: bot.entity.position.z + (Math.random() - 0.5) * distance,
        };
    }

    cx /= totalWeight;
    cz /= totalWeight;

    let dx = bot.entity.position.x - cx;
    let dz = bot.entity.position.z - cz;
    const len = Math.sqrt(dx * dx + dz * dz) || 1;

    return {
        x: bot.entity.position.x + (dx / len) * distance,
        z: bot.entity.position.z + (dz / len) * distance,
    };
}
