// js/lib/utils/threats.ts
import { type Bot } from "mineflayer";
import * as config from "../config.js";
import * as models from "../models.js";

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
    "warden",
]);

export function getThreats(bot: Bot): models.ThreatInfo[] {
    const threats: models.ThreatInfo[] = [];
    const botPos = bot.entity.position;

    for (const id in bot.entities) {
        const e = bot.entities[id];

        if (!e || !e.isValid || e === bot.entity) continue;

        if (
            (e.type !== "mob" && e.type !== "hostile") ||
            !HOSTILE_MOBS.has(e.name!)
        )
            continue;

        // Apply a heavy vertical discount to prevent panicking over mobs in caves
        // when the bot is safely on the surface (and vice versa).
        const dy = Math.abs(botPos.y - e.position.y);
        const dx = botPos.x - e.position.x;
        const dz = botPos.z - e.position.z;

        const horizontalDist = Math.sqrt(dx * dx + dz * dz);
        let effectiveDistance = horizontalDist;

        if (dy > 3) {
            effectiveDistance += dy * 2.5;
        }

        if (effectiveDistance > 16) continue;

        const baseThreat =
            config.THREAT_WEIGHTS[e.name?.toLowerCase() || ""] || 5;
        const threatScore = baseThreat * (10 / Math.max(effectiveDistance, 1));

        threats.push({
            id: e.id,
            name: e.name || "unknown",
            distance: parseFloat(effectiveDistance.toFixed(1)),
            threatScore: Math.round(threatScore),
            position: { x: e.position.x, y: e.position.y, z: e.position.z },
            entity: e,
        });
    }

    return threats.sort((a, b) => b.threatScore! - a.threatScore!);
}

export function computeSafeRetreat(
    bot: Bot,
    threats: models.ThreatInfo[],
    distance: number = 20,
) {
    let cx = 0,
        cz = 0,
        totalWeight = 0;

    for (const threat of threats) {
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
