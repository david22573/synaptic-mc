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
        if (this.checkInterval) clearInterval(this.checkInterval);
        this.checkInterval = null;
    }

    public reset() {
        this.isPanicking = false;
        this.lastDangerAt = 0;
        this.lastFleeTime = 0;
        this.diggingEscape = false;
    }

    public isPanickingNow(): boolean {
        return this.isPanicking;
    }

    private checkSurvival() {
        if (!this.bot || !this.bot.entity) return;

        // Skip threat checks if we are actively executing the dig escape sequence
        if (this.diggingEscape) return;

        const threats = getThreats(this.bot);

        // Relaxed the filter to track ALL hostile mobs within 12 blocks,
        // not just when the bot is at low health.
        const immediateThreats = threats.filter(
            (t: any) =>
                t.distance < 12 &&
                t.threatScore > 5 &&
                t.name !== "low_health_no_food" &&
                t.name !== "starvation",
        );

        if (immediateThreats.length > 0) {
            const topThreat = immediateThreats[0]!;

            // Assess our combat viability
            const hasWeapon = this.bot.inventory
                .items()
                .some(
                    (i) => i.name.includes("axe") || i.name.includes("sword"),
                );

            const isOneOnOne = immediateThreats.length === 1;
            const isUnavoidableDeath =
                topThreat.name === "creeper" || topThreat.name === "warden";

            // If the odds are good, suppress the panic reflex and let Go dispatch the hunt task
            if (
                hasWeapon &&
                isOneOnOne &&
                this.bot.health > 10 &&
                !isUnavoidableDeath
            ) {
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

            // --- STANDARD EVASION LOGIC ---
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

            // Prevent event loop thrashing by only calculating paths every 5s or if stopped
            if (
                !this.bot.pathfinder.isMoving() ||
                Date.now() - this.lastFleeTime > 5000
            ) {
                this.lastFleeTime = Date.now();
                const safePos = computeSafeRetreat(
                    this.bot,
                    immediateThreats,
                    24,
                );
                this.fleeTo(safePos);
            }
        } else if (this.isPanicking && Date.now() > this.lastDangerAt + 3000) {
            this.isPanicking = false;
            log.debug("Survival goal reached; releasing reflex lock");
            this.config.stopMovement();
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
            // First attempt to pathfind normally
            const goal = new goals.GoalNearXZ(safePos.x, safePos.z, 2);

            // If pathfinder returns a partial path or fails immediately, trigger dig escape
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
            // The ultimate panic button: dig a 3-deep hole straight down.
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

            // Try to roof ourselves in with a trash block
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

                // Grab a block at eye level to attach our roof to
                const sideBlock = this.bot.blockAt(
                    this.bot.entity.position.floored().offset(1, 2, 0),
                );

                if (sideBlock && sideBlock.name !== "air") {
                    await this.bot
                        .placeBlock(sideBlock, new Vec3(-1, 0, 0))
                        .catch(() => {});
                } else {
                    const altAnchor = this.bot.blockAt(
                        this.bot.entity.position.floored().offset(-1, 2, 0),
                    );

                    if (altAnchor && altAnchor.name !== "air") {
                        await this.bot
                            .placeBlock(altAnchor, new Vec3(1, 0, 0))
                            .catch(() => {});
                    }
                }
            }
        } catch (err) {
            log.warn("Vertical dig escape failed", { err });
        } finally {
            this.diggingEscape = false;
        }
    }
}
