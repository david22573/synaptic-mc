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
        this.bot.on("physicsTick", () => {
            const now = Date.now();
            if (now - this.lastTickTime < 500) return;
            this.lastTickTime = now;
            void this.evaluatePriorities();
        });

        this.bot.on("goal_reached", () => {
            if (this.reflexActive) {
                // Prevent jitter: Don't drop lock if we are still critically low and starving
                if (this.bot.health < 6 && !this.getBestFood()) {
                    log.debug(
                        "Survival goal reached, but health is still critical. Keeping reflex lock.",
                    );
                    return;
                }
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
        const inImmediateDanger = topThreat && topThreat.threatScore > 50;

        // 1. Immediate external threat overrides everything. Flee!
        if (inImmediateDanger) {
            this.triggerFlee(threats, topThreat.name);
            return;
        }

        // 2. Immediate point-blank combat defense
        if (topThreat && topThreat.distance < 6) {
            const hasWeapon = this.getBestWeapon();
            if (hasWeapon) {
                await this.triggerDefend(topThreat);
                return;
            }
        }

        // 3. Eat if food is low OR if health is low (to trigger passive healing)
        // Only execute this if we aren't actively running from a high-threat mob
        if (
            this.bot.food < 15 ||
            (this.bot.health < 20 && this.bot.food < 20)
        ) {
            const food = this.getBestFood();
            if (food) {
                await this.triggerEat(food);
                return;
            }
        }

        // 4. If we have absolutely no food and health is critically low,
        // trigger a retreat to a safer static area as a last resort
        if (this.bot.health < 6 && !this.getBestFood()) {
            this.triggerFlee(threats, "low_health_no_food");
            return;
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
            new goals.GoalNearXZ(safePos.x, safePos.z, 2),
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
