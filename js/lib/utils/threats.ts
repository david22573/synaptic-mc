import { type Bot } from "mineflayer";
import * as config from "../config.js";
import * as models from "../models.js";

export function getThreats(bot: Bot): models.ThreatInfo[] {
    return Object.values(bot.entities)
        .filter(
            (e: any) => (e.type === "mob" || e.type === "hostile") && e.name,
        )
        .map((e: any) => {
            const distance = bot.entity.position.distanceTo(e.position);
            return { e, distance };
        })
        .filter(({ distance }) => distance <= 16)
        .map(({ e, distance }) => {
            const baseThreat =
                config.THREAT_WEIGHTS[e.name?.toLowerCase() || ""] || 5;
            const threatScore = baseThreat * (10 / Math.max(distance, 1));

            return {
                id: e.id!,
                name: e.name || "unknown",
                distance: parseFloat(distance.toFixed(1)),
                threatScore: Math.round(threatScore),
                position: { x: e.position.x, y: e.position.y, z: e.position.z },
                entity: e,
            };
        })
        .sort((a, b) => b.threatScore - a.threatScore);
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
        cx += threat.position.x * threat.threatScore;
        cz += threat.position.z * threat.threatScore;
        totalWeight += threat.threatScore;
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
