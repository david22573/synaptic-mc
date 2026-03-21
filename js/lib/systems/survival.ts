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

        const immediateThreats = threats.filter(
            (t: any) =>
                t.distance < 12 &&
                t.threatScore > 5 &&
                t.name !== "low_health_no_food" &&
                t.name !== "starvation" &&
                (this.bot.health <= 10 ||
                    t.name === "creeper" ||
                    t.name === "warden"),
        );

        if (immediateThreats.length > 0) {
            this.lastDangerAt = Date.now();

            if (!this.isPanicking) {
                this.isPanicking = true;

                const topThreat = immediateThreats[0]!;
                if (!topThreat) return;

                log.warn("Reflex: Critical Flee triggered", {
                    cause: topThreat.name,
                    health: Math.round(this.bot.health),
                    threatCount: immediateThreats.length,
                });

                this.config.onInterrupt("panic_flee");
                this.config.stopMovement();
                this.client.sendEvent(
                    "panic_retreat",
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
