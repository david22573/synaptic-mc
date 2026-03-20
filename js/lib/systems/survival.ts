import { type Bot } from "mineflayer";
import pkg from "mineflayer-pathfinder";

import * as config from "../config.js";
import * as models from "../models.js";
import { log } from "../logger.js";
import { ControlPlaneClient } from "../network/client.js";
import { getThreats, computeSafeRetreat } from "../utils/threats.js";

const { goals } = pkg;

export interface SurvivalConfig {
    onInterrupt: (reason: string) => void;
    stopMovement: () => void;
}

export class SurvivalSystem {
    private bot: Bot;
    private client: ControlPlaneClient;
    private config: SurvivalConfig;

    private reflexActive = false;
    private lastTickTime = 0;
    private reflexTimeout: NodeJS.Timeout | null = null;

    constructor(
        bot: Bot,
        client: ControlPlaneClient,
        sysConfig: SurvivalConfig,
    ) {
        this.bot = bot;
        this.client = client;
        this.config = sysConfig;
    }

    public start(): void {
        this.bot.on("physicTick", () => {
            const now = Date.now();
            if (now - this.lastTickTime < 500) return;
            this.lastTickTime = now;
            void this.evaluatePriorities();
        });

        this.bot.on("goal_reached", () => {
            if (this.reflexActive) {
                log.debug("Survival goal reached; releasing reflex lock");
                this.clearReflexState();
            }
        });
    }

    private clearReflexState(): void {
        this.reflexActive = false;
        if (this.reflexTimeout) {
            clearTimeout(this.reflexTimeout);
            this.reflexTimeout = null;
        }
    }

    private async evaluatePriorities(): Promise<void> {
        if (this.reflexActive) return;

        const threats = getThreats(this.bot);
        const topThreat = threats.length > 0 ? threats[0] : null;

        if (this.bot.health < 6 || (topThreat && topThreat.threatScore > 50)) {
            const cause = topThreat?.name || "low_health";
            this.triggerFlee(threats, cause);
            return;
        }

        if (topThreat && topThreat.distance < 6) {
            const hasWeapon = this.getBestWeapon();
            if (hasWeapon) {
                await this.triggerDefend(topThreat);
                return;
            }
        }

        if (this.bot.food < 15) {
            const food = this.getBestFood();
            if (food) {
                await this.triggerEat(food);
                return;
            }
        }
    }

    // ==========================================
    // REFLEX ACTIONS
    // ==========================================

    private triggerFlee(threats: models.ThreatInfo[], cause: string): void {
        log.warn("Reflex: Critical Flee triggered", {
            cause,
            health: this.bot.health,
        });

        this.reflexActive = true;
        this.config.onInterrupt("panic_flee");
        this.config.stopMovement();

        this.client.sendEvent(
            "panic_retreat",
            "evasion",
            "",
            cause,
            Date.now(),
        );

        const safePos = computeSafeRetreat(this.bot, threats);
        (this.bot as any).pathfinder.setGoal(
            new goals.GoalNear(
                safePos.x,
                this.bot.entity.position?.y ?? 64,
                safePos.z,
                2,
            ),
        );

        this.reflexTimeout = setTimeout(() => {
            this.config.stopMovement();
            this.clearReflexState();
        }, 8000);
    }

    private async triggerDefend(threat: models.ThreatInfo): Promise<void> {
        log.info("Reflex: Auto-defending", { target: threat.name });

        this.reflexActive = true;
        this.config.onInterrupt("auto_defend");
        this.config.stopMovement();

        try {
            const weapon = this.getBestWeapon();
            if (weapon) {
                await this.bot.equip(weapon, "hand");
            }

            if (threat.entity && threat.entity.position) {
                await this.bot.lookAt(
                    threat.entity.position.offset(
                        0,
                        threat.entity.height ?? 1.5,
                        0,
                    ),
                    true,
                );
                this.bot.attack(threat.entity);
            }
        } catch (err) {
            log.error("Auto-defend failed", { err: String(err) });
        } finally {
            setTimeout(() => this.clearReflexState(), 1000);
        }
    }

    private async triggerEat(foodItem: any): Promise<void> {
        log.info("Reflex: Auto-eating", { food: foodItem.name });

        this.reflexActive = true;
        this.config.onInterrupt("auto_eat");
        this.config.stopMovement();

        try {
            await this.bot.equip(foodItem, "hand");
            await this.bot.consume();
        } catch (err) {
            log.error("Auto-eat failed", { err: String(err) });
        } finally {
            this.clearReflexState();
        }
    }

    // ==========================================
    // UTILS
    // ==========================================

    private getBestWeapon(): any {
        const items = this.bot.inventory.items();
        return (
            items.find((i) => i.name.includes("sword")) ||
            items.find((i) => i.name.includes("axe")) ||
            null
        );
    }

    private getBestFood(): any {
        const items = this.bot.inventory.items();
        const edibleNames = [
            "apple",
            "bread",
            "beef",
            "porkchop",
            "chicken",
            "mutton",
            "carrot",
            "potato",
        ];
        return (
            items.find((i) =>
                edibleNames.some((name) => i.name.includes(name)),
            ) || null
        );
    }
}
