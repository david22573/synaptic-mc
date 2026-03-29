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

    let baseAngle = 0;
    const botPos = bot.entity.position.floored();

    if (totalWeight === 0) {
        baseAngle = Math.random() * Math.PI * 2;
    } else {
        cx /= totalWeight;
        cz /= totalWeight;
        baseAngle = Math.atan2(botPos.z - cz, botPos.x - cx);
    }

    // Fan out and test candidate angles to avoid cliffs and water
    const testAngles = [
        baseAngle,
        baseAngle + Math.PI / 6, // 30 deg
        baseAngle - Math.PI / 6,
        baseAngle + Math.PI / 3, // 60 deg
        baseAngle - Math.PI / 3,
        baseAngle + Math.PI / 2, // 90 deg
        baseAngle - Math.PI / 2,
    ];

    for (const angle of testAngles) {
        const tx = botPos.x + Math.cos(angle) * distance;
        const tz = botPos.z + Math.sin(angle) * distance;

        let isSafe = false;

        // Check for solid ground from slightly above to slightly below current elevation
        for (let yOffset = 2; yOffset >= -5; yOffset--) {
            const block = bot.blockAt(
                botPos.offset(
                    Math.cos(angle) * distance,
                    yOffset,
                    Math.sin(angle) * distance,
                ),
            );

            if (block && (block.name === "water" || block.name === "lava")) {
                // Liquid hazard detected before solid ground, abort this angle
                break;
            }

            if (block && block.boundingBox === "block") {
                // Found a solid block. Ensure the block above it is air so we don't path into a wall
                const above = bot.blockAt(
                    botPos.offset(
                        Math.cos(angle) * distance,
                        yOffset + 1,
                        Math.sin(angle) * distance,
                    ),
                );
                if (
                    above &&
                    (above.name === "air" || above.name === "cave_air")
                ) {
                    isSafe = true;
                    break;
                }
            }
        }

        if (isSafe) {
            return { x: tx, z: tz };
        }
    }

    // Fallback: If all angles look like hazards, just return the exact opposite vector and let pathfinder try to sort it out
    return {
        x: botPos.x + Math.cos(baseAngle) * distance,
        z: botPos.z + Math.sin(baseAngle) * distance,
    };
}
