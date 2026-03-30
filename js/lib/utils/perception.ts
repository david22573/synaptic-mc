import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import * as models from "../models.js";

function getDirectionLabel(
    lookDir: Vec3,
    targetDir: Vec3,
    dot: number,
): string {
    if (dot > 0.8) return "center";
    if (dot < -0.2) return "behind";

    const crossY = lookDir.z * targetDir.x - lookDir.x * targetDir.z;
    return crossY > 0 ? "left" : "right";
}

// Cache 1: Spatial cache for heavy block searches
let staticBlockCache: Vec3[] = [];
let lastBlockSearchPos: Vec3 | null = null;

// Cache 2: Dirty-flag cache for final POI vectors and entities
let poiCache: any[] = [];
let lastPos: Vec3 | null = null;
let lastYaw: number = 0;
let lastUpdate: number = 0;

export function getPOIs(bot: Bot, radius: number = 32): any[] {
    if (!bot.entity) return [];

    const now = Date.now();
    const pos = bot.entity.position;
    const yaw = bot.entity.yaw;

    // Dirty Flag Check: Recompute if we moved > 0.5 blocks, turned our head > ~8.5 degrees,
    // or if 1 second passed (safety TTL to catch moving entities like zombies).
    const isDirty =
        !lastPos ||
        pos.distanceTo(lastPos) > 0.5 ||
        Math.abs(yaw - lastYaw) > 0.15 ||
        now - lastUpdate > 1000;

    if (!isDirty) {
        return poiCache;
    }

    const pois: any[] = [];
    const lookDir = new Vec3(-Math.sin(yaw), 0, -Math.cos(yaw)).normalize();

    // 1. Process Dynamic Entities (Cheap to iterate)
    for (const id in bot.entities) {
        const e = bot.entities[id];
        if (!e || e === bot.entity || !e.isValid) continue;

        const dist = pos.distanceTo(e.position);
        if (dist > radius) continue;

        const dx = e.position.x - pos.x;
        const dz = e.position.z - pos.z;
        const dirToEntity = new Vec3(dx, 0, dz).normalize();

        const visibility = parseFloat(lookDir.dot(dirToEntity).toFixed(2));
        const direction = getDirectionLabel(lookDir, dirToEntity, visibility);

        let type = "entity";
        if (e.type === "hostile") type = "threat";
        else if (e.type === "object" || e.type === "orb") type = "resource";

        const baseScore = 100 / Math.max(dist, 1);
        const scoreMultiplier =
            visibility > 0.5 ? 1.5 : visibility < 0 ? 0.5 : 1.0;
        const score = Math.round(baseScore * scoreMultiplier);

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
            direction,
        });
    }

    // 2. Process Static Blocks (Expensive, uses spatial caching)
    // Only re-run findBlocks if the bot physically moves > 3 blocks from the last search center
    if (!lastBlockSearchPos || pos.distanceTo(lastBlockSearchPos) > 3.0) {
        staticBlockCache = bot.findBlocks({
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
        lastBlockSearchPos = pos.clone();
    }

    for (const bPos of staticBlockCache) {
        const block = bot.blockAt(bPos);
        if (!block) continue;

        const dist = pos.distanceTo(bPos);
        if (dist > radius) continue; // Skip if it fell out of radius due to bot movement

        const dx = bPos.x - pos.x;
        const dz = bPos.z - pos.z;
        const dirToBlock = new Vec3(dx, 0, dz).normalize();

        const visibility = parseFloat(lookDir.dot(dirToBlock).toFixed(2));
        const direction = getDirectionLabel(lookDir, dirToBlock, visibility);

        const baseScore = 100 / Math.max(dist, 1);
        const scoreMultiplier =
            visibility > 0.5 ? 1.5 : visibility < 0 ? 0.5 : 1.0;
        const score = Math.round(baseScore * scoreMultiplier);

        pois.push({
            type: "resource",
            name: block.name,
            position: { x: bPos.x, y: bPos.y, z: bPos.z },
            distance: parseFloat(dist.toFixed(1)),
            visibility,
            score,
            direction,
        });
    }

    pois.sort((a, b) => b.score - a.score);

    const seenCounts: Record<string, number> = {};
    const diversePOIs: any[] = [];

    for (const poi of pois) {
        seenCounts[poi.name] = (seenCounts[poi.name] || 0) + 1;
        if (poi.type === "resource" && seenCounts[poi.name]! > 3) {
            continue;
        }

        diversePOIs.push(poi);
        if (diversePOIs.length >= 15) break;
    }

    // Update dirty flags
    lastPos = pos.clone();
    lastYaw = yaw;
    lastUpdate = now;
    poiCache = diversePOIs;

    return diversePOIs;
}
