import type { Bot } from "mineflayer";
import type { SynapticClient } from "../network/client.js";
import { log } from "../logger.js";
import { getThreats, computeSafeRetreat } from "../utils/threats.js";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";
const { goals } = pkg;

export interface SurvivalConfig {
    onInterrupt: (reason: string) => void;
    stopMovement: () => void;
}

export class SurvivalSystem {
    public bot: Bot;
    private client: SynapticClient;
    private config: SurvivalConfig;
    private running = false;
    private isPanicking = false;
    private lastDangerAt = 0;
    private lastFleeTime = 0;
    private diggingEscape = false;
    private pillaringOut = false;
    private panicCheckCount = 0;
    private lastPos: Vec3 | null = null;
    private tickTimeout: NodeJS.Timeout | null = null;

    constructor(bot: Bot, client: SynapticClient, config: SurvivalConfig) {
        this.bot = bot;
        this.client = client;
        this.config = config;
    }

    public start() {
        if (this.running) return;
        this.running = true;

        const tick = async () => {
            if (!this.running) return;

            if (!this.bot?.entity || this.bot.health <= 0) {
                if (this.isPanicking) {
                    this.reset();
                }
                this.tickTimeout = setTimeout(tick, 5000);
                return;
            }

            try {
                await this.checkSurvival();
            } catch (err) {
                log.error("Error in survival loop tick", {
                    err: err instanceof Error ? err.message : String(err),
                });
            }

            if (this.running) {
                this.tickTimeout = setTimeout(tick, 1000);
            }
        };

        tick();
    }

    public stop() {
        this.running = false;
        if (this.tickTimeout) clearTimeout(this.tickTimeout);
        this.reset();
    }

    public reset() {
        this.isPanicking = false;
        this.lastDangerAt = 0;
        this.lastFleeTime = 0;
        this.diggingEscape = false;
        this.pillaringOut = false;
        this.panicCheckCount = 0;
        this.lastPos = null;
    }

    public isPanickingNow(): boolean {
        return this.isPanicking;
    }

    private async checkSurvival() {
        if (!this.bot?.entity || this.bot.health <= 0) return;
        if (this.diggingEscape || this.pillaringOut) return;

        const threats = getThreats(this.bot);
        // Immediate threats are closer and higher score
        const immediateThreats = threats.filter(
            (t: any) =>
                t.distance < 24 &&
                t.threatScore > 5 &&
                t.name !== "low_health_no_food" &&
                t.name !== "starvation",
        );

        if (immediateThreats.length > 0) {
            const topThreat = immediateThreats[0]!;

            const hasWeapon = this.bot.inventory
                .items()
                .some(
                    (i) => i.name.includes("axe") || i.name.includes("sword"),
                );

            // Logic to stand and fight if we are healthy and armed
            const isOneOnOne = immediateThreats.length === 1;
            const isUnavoidableDeath =
                topThreat.name === "creeper" || topThreat.name === "warden";

            if (
                hasWeapon &&
                isOneOnOne &&
                this.bot.health > 10 &&
                !isUnavoidableDeath
            ) {
                if (this.isPanicking) {
                    this.isPanicking = false;
                    this.panicCheckCount = 0;
                    this.config.stopMovement();
                    if (this.bot.health > 0) {
                        this.client.sendEvent("panic_retreat_end", {
                            status: "RESOLVED",
                            cause: "engaging_in_combat",
                        });
                    }
                }
                return;
            }

            this.lastDangerAt = Date.now();

            const currentPos = this.bot.entity.position.clone();
            if (this.lastPos && currentPos.distanceTo(this.lastPos) < 0.5) {
                this.panicCheckCount++;
            } else {
                this.panicCheckCount = Math.max(0, this.panicCheckCount - 1);
            }
            this.lastPos = currentPos;

            if (this.panicCheckCount > 15) {
                log.warn(
                    "Panic resolution forced: Bot is physically stuck during flee",
                    { count: this.panicCheckCount },
                );
                this.isPanicking = false;
                this.panicCheckCount = 0;
                this.config.stopMovement();
                if (this.bot.health > 0) {
                    this.client.sendPanic(
                        new Error(
                            "Panic loop detected: Bot is stuck or unreachable destination",
                        ),
                    );
                }
                // Emergency: try to dig down if stuck
                await this.digEscape();
                return;
            }

            if (!this.isPanicking) {
                this.isPanicking = true;
                log.warn("Reflex: Critical Flee triggered", {
                    cause: topThreat.name,
                    health: Math.round(this.bot.health),
                    threatCount: immediateThreats.length,
                });
                this.config.onInterrupt("panic_flee");
                if (this.bot.health > 0) {
                    this.client.sendPanic(
                        new Error(`Panic: ${topThreat.name}`),
                    );
                }
            }

            // Persistence: recalculate path every 5 seconds if still in danger
            if (
                !this.bot.pathfinder.isMoving() ||
                Date.now() - this.lastFleeTime > 5000
            ) {
                this.lastFleeTime = Date.now();
                const safePos = computeSafeRetreat(
                    this.bot,
                    immediateThreats,
                    32,
                );
                this.fleeTo(safePos);
            }
        } else if (this.isPanicking && Date.now() > this.lastDangerAt + 5000) {
            // No threats for 5 seconds, clear panic
            this.isPanicking = false;
            this.panicCheckCount = 0;
            this.lastPos = null;
            log.info("Survival goal reached; releasing reflex lock");
            this.config.stopMovement();

            await this.pillarOut().catch((err) =>
                log.warn("Pillar out failed", { err }),
            );

            if (this.bot.health > 0) {
                this.client.sendEvent("panic_retreat_end", {
                    status: "RESOLVED",
                    cause: "safe",
                });
            }
        } else if (!this.isPanicking) {
            this.panicCheckCount = 0;
            this.lastPos = null;
        }
    }

    private async fleeTo(safePos: { x: number; z: number }) {
        try {
            const goal = new goals.GoalNearXZ(safePos.x, safePos.z, 2);
            if (this.bot.pathfinder) {
                this.bot.pathfinder.setGoal(goal);
            }
        } catch (e) {
            log.warn("Pathfinder failed during flee, attempting vertical dig", {
                err: e,
            });
            await this.digEscape();
        }
    }

    private async digEscape() {
        if (this.diggingEscape) return;

        const inWater =
            this.bot.blockAt(this.bot.entity.position.floored())?.name ===
            "water";
        if (inWater) return;

        this.diggingEscape = true;
        this.config.stopMovement();

        try {
            log.info("Initiating emergency vertical dig escape");
            for (let i = 0; i < 3; i++) {
                if (this.bot.health <= 0) break;
                const pos = this.bot.entity.position.floored();
                const below = this.bot.blockAt(pos.offset(0, -1, 0));
                if (
                    below &&
                    below.name !== "air" &&
                    below.name !== "bedrock" &&
                    !["water", "lava"].includes(below.name)
                ) {
                    const tool = this.bot.pathfinder.bestHarvestTool(below);
                    if (tool) await this.bot.equip(tool.type, "hand");
                    await this.bot.dig(below);
                }
            }

            // Try to seal the roof
            const trashBlock = this.bot.inventory
                .items()
                .find((i) =>
                    ["dirt", "cobblestone", "stone", "netherrack"].includes(
                        i.name,
                    ),
                );

            if (trashBlock) {
                await this.bot.equip(trashBlock.type, "hand");
                const pos = this.bot.entity.position.floored();
                const wallBlock = this.bot.blockAt(pos.offset(1, 2, 0));
                if (wallBlock && wallBlock.boundingBox === "block") {
                    await this.bot
                        .placeBlock(wallBlock, new Vec3(-1, 0, 0))
                        .catch(() => {});
                }
            }
        } catch (err) {
            log.warn("Vertical dig escape failed", { err });
        } finally {
            this.diggingEscape = false;
        }
    }

    private async pillarOut() {
        if (this.pillaringOut) return;
        this.pillaringOut = true;

        try {
            if (!this.bot.entity || this.bot.health <= 0) return;

            const isEnclosed = (yOffset: number) => {
                const pos = this.bot.entity.position.floored();
                const blocks = [
                    this.bot.blockAt(pos.offset(0, yOffset, -1)),
                    this.bot.blockAt(pos.offset(0, yOffset, 1)),
                    this.bot.blockAt(pos.offset(1, yOffset, 0)),
                    this.bot.blockAt(pos.offset(-1, yOffset, 0)),
                ];
                return (
                    blocks.filter((b) => b?.boundingBox === "block").length >= 3
                );
            };

            if (!isEnclosed(0) && !isEnclosed(1)) return;

            log.info("Bot appears trapped. Initiating pillar-out recovery.");
            let jumps = 0;
            // LIMIT TO 3 JUMPS MAX. Any higher, and pathfinder panics about lethal fall distance
            while (jumps < 3) {
                jumps++;
                if (this.bot.health <= 0) return;
                if (!isEnclosed(0) && !isEnclosed(1)) break;

                const trashBlock = this.bot.inventory
                    .items()
                    .find((i) =>
                        ["dirt", "cobblestone", "stone", "netherrack"].includes(
                            i.name,
                        ),
                    );
                if (!trashBlock) break;

                // Clear roof if needed
                const pos = this.bot.entity.position.floored();
                const roof = this.bot.blockAt(pos.offset(0, 2, 0));
                if (
                    roof &&
                    roof.name !== "air" &&
                    !["water", "lava"].includes(roof.name)
                ) {
                    const tool = this.bot.pathfinder.bestHarvestTool(roof);
                    if (tool) await this.bot.equip(tool.type, "hand");
                    await this.bot.dig(roof).catch(() => {});
                }

                await this.bot.equip(trashBlock.type, "hand");
                this.bot.setControlState("jump", true);
                await new Promise((r) => setTimeout(r, 300));

                try {
                    const refBlock = this.bot.blockAt(
                        this.bot.entity.position.floored().offset(0, -1, 0),
                    );
                    if (refBlock)
                        await this.bot.placeBlock(refBlock, new Vec3(0, 1, 0));
                } catch (e) {}

                this.bot.setControlState("jump", false);
                await new Promise((r) => setTimeout(r, 200));
            }
        } finally {
            this.bot.setControlState("jump", false);
            this.pillaringOut = false;
        }
    }
}
