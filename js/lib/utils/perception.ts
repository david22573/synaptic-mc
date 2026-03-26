import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import * as models from "../models.js";

export function getPOIs(bot: Bot, radius: number = 32): models.POI[] {
    const pois: models.POI[] = [];
    if (!bot.entity) return pois;

    const pos = bot.entity.position;
    const yaw = bot.entity.yaw;

    // Horizontal look vector
    const lookDir = new Vec3(-Math.sin(yaw), 0, -Math.cos(yaw)).normalize();

    // 1. Entities (Mobs, Animals, Items)
    for (const id in bot.entities) {
        const e = bot.entities[id];
        if (!e || e === bot.entity || !e.isValid) continue;

        const dist = pos.distanceTo(e.position);
        if (dist > radius) continue;

        const dx = e.position.x - pos.x;
        const dz = e.position.z - pos.z;
        const dirToEntity = new Vec3(dx, 0, dz).normalize();

        // Dot product: 1.0 (dead center), 0.0 (perpendicular), -1.0 (behind)
        const visibility = parseFloat(lookDir.dot(dirToEntity).toFixed(2));

        let type = "entity";
        if (e.type === "mob") type = "threat";
        else if (e.type === "animal") type = "opportunity";
        else if (e.type === "object" || e.type === "item") type = "resource";

        const baseScore = 100 / Math.max(dist, 1);
        const score = Math.round(baseScore * (visibility > 0.5 ? 1.5 : 1.0));

        pois.push({
            type,
            name: e.name || "unknown",
            position: {
                x: Math.round(e.position.x),
                y: Math.round(e.position.y),
                z: Math.round(e.position.z),
            },
            distance: parseFloat(dist.toFixed(1)),
            visibility,
            score,
        });
    }

    // 2. Blocks (Filtered strictly to avoid heavy tick lag)
    const blocks = bot.findBlocks({
        matching: (b) => {
            if (!b) return false;
            return (
                b.name.endsWith("_log") ||
                b.name.includes("ore") ||
                b.name === "stone" ||
                b.name === "crafting_table" ||
                b.name.includes("bed") ||
                b.name === "water" ||
                b.name === "lava"
            );
        },
        maxDistance: radius,
        count: 24,
    });

    for (const bPos of blocks) {
        const block = bot.blockAt(bPos);
        if (!block) continue;

        const dist = pos.distanceTo(bPos);
        const dx = bPos.x - pos.x;
        const dz = bPos.z - pos.z;
        const dirToBlock = new Vec3(dx, 0, dz).normalize();
        const visibility = parseFloat(lookDir.dot(dirToBlock).toFixed(2));

        const score = Math.round(
            (100 / Math.max(dist, 1)) * (visibility > 0.5 ? 1.5 : 1.0),
        );

        pois.push({
            type: "resource",
            name: block.name,
            position: { x: bPos.x, y: bPos.y, z: bPos.z },
            distance: parseFloat(dist.toFixed(1)),
            visibility,
            score,
        });
    }

    // Sort by computed score (closest + most visible at the top)
    return pois.sort((a, b) => b.score - a.score).slice(0, 15);
}
