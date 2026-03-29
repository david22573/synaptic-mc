import type { Bot } from "mineflayer";
import type { ControlPlaneClient } from "../network/client.js";
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
    private client: ControlPlaneClient;
    private config: SurvivalConfig;
    private checkInterval: NodeJS.Timeout | null = null;
    private isPanicking = false;
    private lastDangerAt = 0;
    private lastFleeTime = 0;
    private diggingEscape = false;
    private pillaringOut = false;

    constructor(bot: Bot, client: ControlPlaneClient, config: SurvivalConfig) {
        this.bot = bot;
        this.client = client;
        this.config = config;
    }

    public start() {
        if (this.checkInterval) clearInterval(this.checkInterval);
        this.checkInterval = setInterval(() => this.checkSurvival(), 1000);
    }

    public stop() {
        if (this.checkInterval) {
            clearInterval(this.checkInterval);
            this.checkInterval = null;
        }
        this.reset();
    }

    public reset() {
        this.isPanicking = false;
        this.lastDangerAt = 0;
        this.lastFleeTime = 0;
        this.diggingEscape = false;
        this.pillaringOut = false;
    }

    public isPanickingNow(): boolean {
        return this.isPanicking;
    }

    private async checkSurvival() {
        if (!this.bot?.entity) return;
        if (this.diggingEscape || this.pillaringOut) return;

        const threats = getThreats(this.bot);
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

            const isOneOnOne = immediateThreats.length === 1;
            const isUnavoidableDeath =
                topThreat.name === "creeper" || topThreat.name === "warden";

            if (
                hasWeapon &&
                isOneOnOne &&
                this.bot.health > 10 &&
                !isUnavoidableDeath
            ) {
                // FIX: Properly reset panic state when engaging
                if (this.isPanicking) {
                    this.isPanicking = false;
                    this.config.stopMovement();
                    this.client.sendEvent(
                        "panic_retreat_end",
                        "evasion_complete",
                        "",
                        "engaging_in_combat",
                        0,
                    );
                }
                return;
            }

            this.lastDangerAt = Date.now();

            if (!this.isPanicking) {
                this.isPanicking = true;
                log.warn("Reflex: Critical Flee triggered", {
                    cause: topThreat.name,
                    health: Math.round(this.bot.health),
                    threatCount: immediateThreats.length,
                });

                this.config.onInterrupt("panic_flee");
                this.config.stopMovement();
                this.client.sendEvent(
                    "panic_retreat_start",
                    "evasion",
                    "",
                    topThreat.name,
                    0,
                );
            }

            // FIX: Prevent spamming flee commands
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
            // FIX: Ensure we reset state properly when safe
            this.isPanicking = false;
            log.debug(
                "Survival goal reached; securing exit and releasing reflex lock",
            );
            this.config.stopMovement();

            this.pillaringOut = true;
            await this.pillarOut().catch((err) =>
                log.warn("Pillar out failed", { err }),
            );
            this.pillaringOut = false;

            this.client.sendEvent(
                "panic_retreat_end",
                "evasion_complete",
                "",
                "safe",
                0,
            );
        }
    }

    private async fleeTo(safePos: { x: number; z: number }) {
        try {
            const goal = new goals.GoalNearXZ(safePos.x, safePos.z, 2);
            const path = this.bot.pathfinder.getPathTo(
                this.bot.pathfinder.movements,
                goal,
            );

            if (path.status === "noPath" || path.status === "timeout") {
                log.warn(
                    "Panic pathfinding failed, attempting vertical dig escape",
                );
                await this.digEscape();
                return;
            }

            this.bot.pathfinder.setGoal(goal);
        } catch (e) {
            log.warn(
                "Pathfinder threw during panic, attempting vertical dig escape",
            );
            await this.digEscape();
        }
    }

    private async digEscape() {
        if (this.diggingEscape) return;

        const inWater =
            this.bot.blockAt(this.bot.entity.position.floored())?.name ===
            "water";
        if (inWater) {
            log.warn("Refusing to vertical dig escape while submerged.");
            return;
        }

        this.diggingEscape = true;
        this.config.stopMovement();

        try {
            for (let i = 0; i < 3; i++) {
                if (this.bot.health <= 0) break;

                const pos = this.bot.entity.position.floored();
                const below = this.bot.blockAt(pos.offset(0, -1, 0));

                if (
                    below &&
                    below.name !== "air" &&
                    below.name !== "bedrock" &&
                    below.name !== "water" &&
                    below.name !== "lava"
                ) {
                    const tool = this.bot.pathfinder.bestHarvestTool(below);
                    if (tool) await this.bot.equip(tool, "hand");
                    await this.bot.dig(below);
                }
            }

            // FIX: Improved roof sealing with better error handling
            const trashBlock = this.bot.inventory
                .items()
                .find((i) =>
                    [
                        "dirt",
                        "cobblestone",
                        "stone",
                        "granite",
                        "diorite",
                        "andesite",
                        "netherrack",
                    ].includes(i.name),
                );

            if (trashBlock) {
                await this.bot.equip(trashBlock, "hand");
                const pos = this.bot.entity.position.floored();
                const compassVectors = [
                    new Vec3(1, 0, 0),
                    new Vec3(-1, 0, 0),
                    new Vec3(0, 0, 1),
                    new Vec3(0, 0, -1),
                ];

                let placed = false;
                for (const vec of compassVectors) {
                    const wallBlock = this.bot.blockAt(
                        pos.offset(vec.x, 2, vec.z),
                    );
                    if (wallBlock && wallBlock.boundingBox === "block") {
                        try {
                            await this.bot.placeBlock(
                                wallBlock,
                                vec.scaled(-1),
                            );
                            placed = true;
                            log.debug(
                                "Successfully sealed roof at surface level.",
                            );
                            break;
                        } catch (e) {}
                    }
                }

                if (!placed) {
                    log.warn(
                        "Failed to seal roof. Terrain geometry unsupported.",
                    );
                }
            }
        } catch (err) {
            log.warn("Vertical dig escape failed", { err });
        } finally {
            this.diggingEscape = false;
        }
    }

    private async pillarOut() {
        if (!this.bot.entity || this.bot.health <= 0) return;

        const isEnclosed = (yOffset: number) => {
            const pos = this.bot.entity.position.floored();
            const blocks = [
                this.bot.blockAt(pos.offset(0, yOffset, -1)),
                this.bot.blockAt(pos.offset(0, yOffset, 1)),
                this.bot.blockAt(pos.offset(1, yOffset, 0)),
                this.bot.blockAt(pos.offset(-1, yOffset, 0)),
            ];
            return blocks.filter((b) => b?.boundingBox === "block").length >= 3;
        };

        if (!isEnclosed(0) && !isEnclosed(1)) {
            return;
        }

        log.info("Trapped in pit. Initiating pillar-out recovery sequence.");

        let jumps = 0;
        while (jumps < 10) {
            jumps++;
            if (this.bot.health <= 0) return;

            const pos = this.bot.entity.position.floored();
            if (!isEnclosed(0) && !isEnclosed(1)) {
                log.info("Surface reached. Resuming normal operations.");
                break;
            }

            const trashBlock = this.bot.inventory
                .items()
                .find((i) =>
                    [
                        "dirt",
                        "cobblestone",
                        "stone",
                        "netherrack",
                        "granite",
                        "diorite",
                        "andesite",
                    ].includes(i.name),
                );

            if (!trashBlock) {
                log.warn("Out of blocks! Cannot pillar out of hole.");
                break;
            }

            // FIX: Break roof before jumping to avoid head collision
            const roof = this.bot.blockAt(pos.offset(0, 2, 0));
            if (
                roof &&
                roof.name !== "air" &&
                roof.name !== "water" &&
                roof.name !== "lava"
            ) {
                const tool = this.bot.pathfinder.bestHarvestTool(roof);
                if (tool) await this.bot.equip(tool, "hand");
                await this.bot.dig(roof).catch(() => {});
            }

            await this.bot.equip(trashBlock, "hand");
            await this.bot.lookAt(pos.offset(0, -1, 0), true);

            const refBlock = this.bot.blockAt(pos.offset(0, -1, 0));
            if (refBlock) {
                this.bot.setControlState("jump", true);
                await new Promise((resolve) => setTimeout(resolve, 300));

                try {
                    await this.bot.placeBlock(refBlock, new Vec3(0, 1, 0));
                } catch (e) {
                    log.warn("Failed to place block under feet", { err: e });
                }

                this.bot.setControlState("jump", false);
                await new Promise((resolve) => setTimeout(resolve, 200));
            }
        }
    }
}
