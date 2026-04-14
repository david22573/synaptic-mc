import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import type * as models from "../models.js";

type ControlName =
    | "forward"
    | "back"
    | "left"
    | "right"
    | "jump"
    | "sprint";

type HazardKind =
    | "lava"
    | "drowning"
    | "burning"
    | "ensnared"
    | "heavy_damage"
    | "cliff";

interface SensorMemory {
    lastHealth: number;
    lastDamage: number;
    lastDamageAt: number;
    submergedSince: number;
    lastEvalAt: number;
    lastYaw: number;
    lastPos: Vec3 | null;
    lastResult: WorldAwareness | null;
}

const PASSABLE_BLOCKS = new Set([
    "air",
    "cave_air",
    "void_air",
    "water",
    "flowing_water",
    "lava",
    "flowing_lava",
    "tall_grass",
    "grass",
    "short_grass",
    "fern",
    "large_fern",
    "vine",
    "cobweb",
    "sweet_berry_bush",
]);

const LIQUID_BLOCKS = new Set([
    "water",
    "flowing_water",
    "lava",
    "flowing_lava",
]);

const FIRE_BLOCKS = new Set([
    "fire",
    "soul_fire",
    "campfire",
    "soul_campfire",
    "magma_block",
]);

const TRAP_BLOCKS = new Set(["cobweb", "sweet_berry_bush"]);

const SENSOR_CACHE_MS = 75;
const SENSOR_MEMORY = new WeakMap<Bot, SensorMemory>();

const EMPTY_CONTROLS: Record<ControlName, boolean> = {
    forward: false,
    back: false,
    left: false,
    right: false,
    jump: false,
    sprint: false,
};

export interface WorldAwareness {
    hazard: HazardKind | null;
    panicCause: string | null;
    feedback: models.Feedback[];
    dangerZones: models.DangerZone[];
    terrainRoughness: Record<string, number>;
    escapeTarget: Vec3 | null;
    recentDamage: number;
    threatName: string | null;
    topThreatDistance: number | null;
    movement: {
        inWater: boolean;
        submerged: boolean;
        inLava: boolean;
        onFire: boolean;
        inCobweb: boolean;
        inBerryBush: boolean;
        frontBlocked: boolean;
        steepDropAhead: boolean;
        fallingFast: boolean;
        lowAir: boolean;
    };
}

export interface WorldControlOverrides {
    controls: Partial<Record<ControlName, boolean>>;
    clearPathfinder: boolean;
    urgent: boolean;
    lookAt: Vec3 | null;
}

function cloneEmptyWorld(): WorldAwareness {
    return {
        hazard: null,
        panicCause: null,
        feedback: [],
        dangerZones: [],
        terrainRoughness: {},
        escapeTarget: null,
        recentDamage: 0,
        threatName: null,
        topThreatDistance: null,
        movement: {
            inWater: false,
            submerged: false,
            inLava: false,
            onFire: false,
            inCobweb: false,
            inBerryBush: false,
            frontBlocked: false,
            steepDropAhead: false,
            fallingFast: false,
            lowAir: false,
        },
    };
}

function isPassable(block: any): boolean {
    if (!block) return true;
    return PASSABLE_BLOCKS.has(block.name) || block.boundingBox === "empty";
}

function isLiquid(block: any): boolean {
    return !!block && LIQUID_BLOCKS.has(block.name);
}

function isWater(block: any): boolean {
    return !!block && (block.name === "water" || block.name === "flowing_water");
}

function isLava(block: any): boolean {
    return !!block && (block.name === "lava" || block.name === "flowing_lava");
}

function isFire(block: any): boolean {
    return !!block && FIRE_BLOCKS.has(block.name);
}

function isSolidGround(block: any): boolean {
    return (
        !!block &&
        block.boundingBox === "block" &&
        !LIQUID_BLOCKS.has(block.name) &&
        !TRAP_BLOCKS.has(block.name)
    );
}

function blockRiskScore(block: any): number {
    if (!block) return 2.5;
    if (isLava(block) || isFire(block)) return 5;
    if (TRAP_BLOCKS.has(block.name)) return 3;
    if (isLiquid(block)) return 2;
    if (block.boundingBox !== "block") return 0.5;
    return 0;
}

function makeDangerZone(
    center: Vec3,
    radius: number,
    reason: string,
    risk: number,
): models.DangerZone {
    return {
        center: { x: center.x, y: center.y, z: center.z },
        radius,
        reason,
        risk,
    };
}

function normalizeYaw(angle: number): number {
    while (angle > Math.PI) angle -= Math.PI * 2;
    while (angle < -Math.PI) angle += Math.PI * 2;
    return angle;
}

function currentChunkKey(pos: Vec3): string {
    return `${Math.floor(pos.x) >> 4},${Math.floor(pos.z) >> 4}`;
}

function computeTerrainRoughness(bot: Bot, feetPos: Vec3): number {
    let samples = 0;
    let risk = 0;

    for (let dx = -1; dx <= 1; dx++) {
        for (let dz = -1; dz <= 1; dz++) {
            if (dx === 0 && dz === 0) continue;

            const candidate = feetPos.offset(dx, 0, dz);
            const below = bot.blockAt(candidate.offset(0, -1, 0));
            const feet = bot.blockAt(candidate);
            const head = bot.blockAt(candidate.offset(0, 1, 0));

            samples++;

            if (!isSolidGround(below)) {
                risk += 1;
                continue;
            }

            if (!isPassable(feet)) risk += 0.75;
            if (!isPassable(head)) risk += 0.5;
            risk += blockRiskScore(feet) * 0.25;
            risk += blockRiskScore(head) * 0.25;
        }
    }

    return Number((risk / Math.max(samples, 1)).toFixed(2));
}

function findStandablePosition(bot: Bot, base: Vec3): Vec3 | null {
    const x = Math.round(base.x);
    const z = Math.round(base.z);
    const baseY = Math.floor(base.y);

    for (let y = baseY + 1; y >= baseY - 2; y--) {
        const feetPos = new Vec3(x, y, z);
        const below = bot.blockAt(feetPos.offset(0, -1, 0));
        const feet = bot.blockAt(feetPos);
        const head = bot.blockAt(feetPos.offset(0, 1, 0));

        if (!isSolidGround(below)) continue;
        if (!isPassable(feet) || !isPassable(head)) continue;
        if (blockRiskScore(feet) >= 4 || blockRiskScore(head) >= 4) continue;

        return feetPos.offset(0.5, 0, 0.5);
    }

    return null;
}

function scoreEscapeCandidate(
    bot: Bot,
    pos: Vec3,
    candidate: Vec3,
    threats: models.ThreatInfo[],
    hazard: HazardKind | null,
    forwardDir: Vec3,
): number {
    const feetPos = candidate.floored();
    const below = bot.blockAt(feetPos.offset(0, -1, 0));
    const feet = bot.blockAt(feetPos);
    const head = bot.blockAt(feetPos.offset(0, 1, 0));
    const heading = candidate.minus(pos);

    let score = candidate.distanceTo(pos) * 1.2;
    score -= Math.abs(candidate.y - pos.y) * 1.5;
    score -= blockRiskScore(below) * 2;
    score -= blockRiskScore(feet) * 2;
    score -= blockRiskScore(head) * 2;

    if (hazard === "burning") {
        const waterNearby = [
            bot.blockAt(feetPos),
            bot.blockAt(feetPos.offset(1, 0, 0)),
            bot.blockAt(feetPos.offset(-1, 0, 0)),
            bot.blockAt(feetPos.offset(0, 0, 1)),
            bot.blockAt(feetPos.offset(0, 0, -1)),
        ].some((block) => isWater(block));
        if (waterNearby) score += 5;
    }

    if (hazard === "cliff") {
        const forwardDot =
            heading.distanceTo(new Vec3(0, 0, 0)) > 0
                ? heading.normalize().dot(forwardDir)
                : 0;
        score -= Math.max(0, forwardDot) * 4;
    }

    if (threats.length > 0) {
        const topThreat = threats[0]!;
        if (topThreat.position) {
            const threatPos = new Vec3(
                topThreat.position.x,
                topThreat.position.y,
                topThreat.position.z,
            );
            score += Math.min(candidate.distanceTo(threatPos), 16) * 0.5;
        }
    }

    return score;
}

function findEscapeTarget(
    bot: Bot,
    pos: Vec3,
    threats: models.ThreatInfo[],
    hazard: HazardKind | null,
): Vec3 | null {
    const yaw = bot.entity.yaw;
    const forwardDir = new Vec3(-Math.sin(yaw), 0, -Math.cos(yaw)).normalize();
    const directions = [
        forwardDir,
        new Vec3(forwardDir.z, 0, -forwardDir.x),
        new Vec3(-forwardDir.z, 0, forwardDir.x),
        forwardDir.scaled(-1),
        forwardDir.plus(new Vec3(forwardDir.z, 0, -forwardDir.x)).normalize(),
        forwardDir.plus(new Vec3(-forwardDir.z, 0, forwardDir.x)).normalize(),
        forwardDir
            .scaled(-1)
            .plus(new Vec3(forwardDir.z, 0, -forwardDir.x))
            .normalize(),
        forwardDir
            .scaled(-1)
            .plus(new Vec3(-forwardDir.z, 0, forwardDir.x))
            .normalize(),
    ];

    let best: Vec3 | null = null;
    let bestScore = -Infinity;

    for (const dir of directions) {
        for (const distance of [2, 3, 4]) {
            const candidateBase = pos.offset(
                Math.round(dir.x * distance),
                0,
                Math.round(dir.z * distance),
            );
            const candidate = findStandablePosition(bot, candidateBase);
            if (!candidate) continue;

            const score = scoreEscapeCandidate(
                bot,
                pos,
                candidate,
                threats,
                hazard,
                forwardDir,
            );

            if (score > bestScore) {
                best = candidate;
                bestScore = score;
            }
        }
    }

    return best;
}

function buildFeedback(
    bot: Bot,
    movement: WorldAwareness["movement"],
    recentDamage: number,
): models.Feedback[] {
    const feedback: models.Feedback[] = [];

    if (movement.inLava) {
        feedback.push({
            type: "hazard",
            cause: "lava_contact",
            action: "retreat",
            hint: "step onto nearest solid block immediately",
        });
    } else if (movement.onFire) {
        feedback.push({
            type: "hazard",
            cause: "burning",
            action: "retreat",
            hint: "seek water or open ground",
        });
    }

    if (movement.lowAir) {
        feedback.push({
            type: "hazard",
            cause: "low_air",
            action: "retreat",
            hint: "surface now",
        });
    } else if (movement.submerged) {
        feedback.push({
            type: "movement",
            cause: "swimming",
            action: "jump",
            hint: "keep head above water",
        });
    }

    if (movement.inCobweb || movement.inBerryBush) {
        feedback.push({
            type: "movement",
            cause: movement.inCobweb ? "cobweb" : "berry_bush",
            action: "jump",
            hint: "free movement before resuming task",
        });
    }

    if (movement.steepDropAhead) {
        feedback.push({
            type: "terrain",
            cause: "cliff_ahead",
            action: "reroute",
            hint: "avoid blind forward sprint",
        });
    }

    if (recentDamage >= 3) {
        feedback.push({
            type: "combat",
            cause: "recent_heavy_damage",
            action: "retreat",
            hint: `lost ${Math.round(recentDamage)} hp recently`,
        });
    }

    return feedback;
}

export function senseWorld(
    bot: Bot,
    threats: models.ThreatInfo[] = [],
): WorldAwareness {
    if (!bot.entity) return cloneEmptyWorld();

    const pos = bot.entity.position;
    const yaw = bot.entity.yaw;
    const now = Date.now();
    const memory =
        SENSOR_MEMORY.get(bot) ??
        ({
            lastHealth: bot.health,
            lastDamage: 0,
            lastDamageAt: 0,
            submergedSince: 0,
            lastEvalAt: 0,
            lastYaw: yaw,
            lastPos: pos.clone(),
            lastResult: null,
        } satisfies SensorMemory);

    if (
        memory.lastResult &&
        now - memory.lastEvalAt < SENSOR_CACHE_MS &&
        memory.lastPos &&
        memory.lastPos.distanceTo(pos) < 0.25 &&
        Math.abs(memory.lastYaw - yaw) < 0.08 &&
        Math.abs(memory.lastHealth - bot.health) < 0.1
    ) {
        return memory.lastResult;
    }

    if (bot.health < memory.lastHealth - 0.1) {
        memory.lastDamage = memory.lastHealth - bot.health;
        memory.lastDamageAt = now;
    } else if (now - memory.lastDamageAt > 3000) {
        memory.lastDamage = 0;
    }

    const recentDamage =
        now - memory.lastDamageAt <= 3000 ? memory.lastDamage : 0;

    const feetPos = pos.floored();
    const dirX = Math.round(-Math.sin(yaw));
    const dirZ = Math.round(-Math.cos(yaw));

    const feetBlock = bot.blockAt(feetPos);
    const headBlock = bot.blockAt(feetPos.offset(0, 1, 0));
    const belowBlock = bot.blockAt(feetPos.offset(0, -1, 0));
    const frontFeetBlock = bot.blockAt(feetPos.offset(dirX, 0, dirZ));
    const frontHeadBlock = bot.blockAt(feetPos.offset(dirX, 1, dirZ));
    const frontBelowBlock = bot.blockAt(feetPos.offset(dirX, -1, dirZ));
    const frontDeepBlock = bot.blockAt(feetPos.offset(dirX, -2, dirZ));

    const inWater =
        Boolean((bot.entity as any).isInWater) ||
        isWater(feetBlock) ||
        isWater(headBlock);
    const submerged = inWater && isWater(headBlock);
    const inLava =
        Boolean((bot.entity as any).isInLava) ||
        isLava(feetBlock) ||
        isLava(headBlock);
    const onFire =
        Boolean((bot.entity as any).isOnFire) ||
        Number((bot.entity as any).fireTicks || 0) > 0 ||
        isFire(feetBlock) ||
        isFire(headBlock);

    if (submerged) {
        if (memory.submergedSince === 0) memory.submergedSince = now;
    } else {
        memory.submergedSince = 0;
    }

    const movement = {
        inWater,
        submerged,
        inLava,
        onFire,
        inCobweb:
            feetBlock?.name === "cobweb" || headBlock?.name === "cobweb",
        inBerryBush:
            feetBlock?.name === "sweet_berry_bush" ||
            headBlock?.name === "sweet_berry_bush",
        frontBlocked:
            !!frontFeetBlock &&
            !isPassable(frontFeetBlock) &&
            frontFeetBlock.boundingBox === "block" ||
            !!frontHeadBlock &&
                !isPassable(frontHeadBlock) &&
                frontHeadBlock.boundingBox === "block",
        steepDropAhead:
            !inWater &&
            !inLava &&
            !isSolidGround(frontBelowBlock) &&
            !isSolidGround(frontDeepBlock),
        fallingFast: bot.entity.velocity.y < -0.55 && !bot.entity.onGround,
        lowAir:
            submerged &&
            memory.submergedSince > 0 &&
            now - memory.submergedSince > 4000,
    };

    const roughness = computeTerrainRoughness(bot, feetPos);
    const threatName = threats[0]?.name || null;
    const topThreatDistance = threats[0]?.distance ?? null;

    let hazard: HazardKind | null = null;
    let panicCause: string | null = null;

    if (movement.inLava) {
        hazard = "lava";
        panicCause = "lava";
    } else if (movement.lowAir) {
        hazard = "drowning";
        panicCause = "drowning";
    } else if (movement.onFire && bot.health <= 12) {
        hazard = "burning";
        panicCause = "burning";
    } else if (
        (movement.inCobweb || movement.inBerryBush) &&
        topThreatDistance !== null &&
        topThreatDistance <= 6
    ) {
        hazard = "ensnared";
        panicCause = "ensnared";
    } else if (recentDamage >= 4 && topThreatDistance !== null && topThreatDistance <= 10) {
        hazard = "heavy_damage";
        panicCause = "under_attack";
    } else if (movement.steepDropAhead) {
        hazard = "cliff";
    }

    const escapeTarget =
        movement.inLava ||
        movement.lowAir ||
        movement.onFire ||
        movement.inCobweb ||
        movement.inBerryBush ||
        movement.steepDropAhead
            ? findEscapeTarget(bot, pos, threats, hazard)
            : null;

    const feedback = buildFeedback(bot, movement, recentDamage);
    const dangerZones: models.DangerZone[] = [];

    if (movement.inLava) {
        dangerZones.push(makeDangerZone(pos, 6, "lava", 1));
    } else if (movement.onFire) {
        dangerZones.push(makeDangerZone(pos, 6, "fire", 0.85));
    }

    if (movement.steepDropAhead) {
        dangerZones.push(
            makeDangerZone(pos.offset(dirX * 2, 0, dirZ * 2), 4, "cliff", 0.7),
        );
    }

    if (
        threats[0]?.position &&
        topThreatDistance !== null &&
        topThreatDistance <= 8
    ) {
        dangerZones.push(
            makeDangerZone(
                new Vec3(
                    threats[0].position.x,
                    threats[0].position.y,
                    threats[0].position.z,
                ),
                8,
                `mob_${threats[0].name}`,
                Math.min(1, (threats[0].threatScore ?? 8) / 10),
            ),
        );
    }

    if (!isSolidGround(belowBlock) && !inWater && !inLava) {
        dangerZones.push(makeDangerZone(pos, 4, "unstable_ground", 0.55));
    }

    const result: WorldAwareness = {
        hazard,
        panicCause,
        feedback,
        dangerZones,
        terrainRoughness: { [currentChunkKey(pos)]: roughness },
        escapeTarget,
        recentDamage,
        threatName,
        topThreatDistance,
        movement,
    };

    memory.lastEvalAt = now;
    memory.lastHealth = bot.health;
    memory.lastYaw = yaw;
    memory.lastPos = pos.clone();
    memory.lastResult = result;
    SENSOR_MEMORY.set(bot, memory);

    return result;
}

function controlsToward(bot: Bot, target: Vec3): Record<ControlName, boolean> {
    const controls = { ...EMPTY_CONTROLS };
    if (!bot.entity) return controls;

    const pos = bot.entity.position;
    const dx = target.x - pos.x;
    const dz = target.z - pos.z;
    const desiredYaw = Math.atan2(-dx, -dz);
    const yawDiff = normalizeYaw(desiredYaw - bot.entity.yaw);

    controls.forward = Math.abs(yawDiff) < Math.PI * 0.75;
    controls.back = Math.abs(yawDiff) > Math.PI * 0.75;
    controls.left = yawDiff > 0.2;
    controls.right = yawDiff < -0.2;
    controls.sprint = !controls.back;

    return controls;
}

export function getWorldControlOverrides(
    bot: Bot,
    world: WorldAwareness,
): WorldControlOverrides {
    const controls: Partial<Record<ControlName, boolean>> = {};
    let lookAt: Vec3 | null = null;
    let urgent = false;
    let clearPathfinder = false;

    if (world.panicCause && world.escapeTarget) {
        urgent = true;
        clearPathfinder = true;
        lookAt = world.escapeTarget.offset(0, 1, 0);
        Object.assign(controls, controlsToward(bot, world.escapeTarget));
        controls.jump = true;
        controls.sprint = true;
        return { controls, clearPathfinder, urgent, lookAt };
    }

    if (world.escapeTarget && world.movement.steepDropAhead) {
        lookAt = world.escapeTarget.offset(0, 1, 0);
    }

    if (
        world.movement.inWater ||
        world.movement.submerged ||
        world.movement.inLava ||
        world.movement.inCobweb ||
        world.movement.inBerryBush ||
        world.movement.frontBlocked
    ) {
        controls.jump = true;
    }

    if (
        world.movement.onFire ||
        world.movement.inLava ||
        world.recentDamage >= 3
    ) {
        controls.sprint = true;
    }

    if (world.movement.steepDropAhead && !world.movement.inWater) {
        controls.forward = false;
        controls.jump = false;
    }

    return { controls, clearPathfinder, urgent, lookAt };
}

export function applyWorldMovementReflexes(
    bot: Bot,
    world: WorldAwareness,
): void {
    const overrides = getWorldControlOverrides(bot, world);

    if (overrides.lookAt) {
        bot.lookAt(overrides.lookAt, true).catch(() => {});
    }

    for (const [control, value] of Object.entries(overrides.controls)) {
        bot.setControlState(control as ControlName, value);
    }
}
