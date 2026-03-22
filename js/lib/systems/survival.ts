// js/lib/systems/survival.ts
import type { Bot } from "mineflayer";
import type { ControlPlaneClient } from "../network/client.js";
import { log } from "../logger.js";
import { getThreats, computeSafeRetreat } from "../utils/threats.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

export interface SurvivalConfig {
    onInterrupt: (reason: string) => void;
    stopMovement: () => void;
}

export class SurvivalSystem {
    private bot: Bot;
    private client: ControlPlaneClient;
    private config: SurvivalConfig;
    private checkInterval: NodeJS.Timeout | null = null;
    private isPanicking = false;
    private lastDangerAt = 0;

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
    }

    public isPanickingNow(): boolean {
        return this.isPanicking;
    }

    private checkSurvival() {
        if (!this.bot || !this.bot.entity) return;

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

            const safePos = computeSafeRetreat(this.bot, immediateThreats, 24);
            this.fleeTo(safePos);
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

    private fleeTo(safePos: { x: number; z: number }) {
        try {
            this.bot.pathfinder.setGoal(
                new goals.GoalNearXZ(safePos.x, safePos.z, 2),
            );
        } catch (e) {
            // Ignore pathing errors during panic overrides
        }
    }
}
