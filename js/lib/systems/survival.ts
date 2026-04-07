import type { Bot } from "mineflayer";
import { log } from "../logger.js";
import { getThreats } from "../utils/threats.js";

export interface SurvivalConfig {
    onInterrupt: (reason: string) => void;
    stopMovement: () => void;
    onPanicStart?: (cause: string) => void;
    onPanicEnd?: (cause: string) => void;
}

export class SurvivalSystem {
    public bot: Bot;
    private config: SurvivalConfig;
    private running = false;
    private isPanicking = false;
    private tickTimeout: NodeJS.Timeout | null = null;
    private panicCooldownUntil = 0;

    constructor(bot: Bot, config: SurvivalConfig) {
        this.bot = bot;
        this.config = config;
    }

    public start() {
        if (this.running) return;
        this.running = true;

        const tick = async () => {
            if (!this.running) return;

            if (this.bot?.entity && this.bot.health > 0) {
                this.checkSurvival();
            }

            if (this.running) {
                // Run four times a second so close-range hostiles trigger faster.
                this.tickTimeout = setTimeout(tick, 250);
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
        this.panicCooldownUntil = 0;
    }

    public isPanickingNow(): boolean {
        return this.isPanicking;
    }

    private checkSurvival() {
        const threats = getThreats(this.bot);

        const criticalMobNames = new Set(["creeper", "skeleton", "witch"]);

        // React earlier when health is already compromised or a high-risk mob is close.
        const immediateThreats = threats.filter(
            (t: any) =>
                t.distance < 20 &&
                (
                    t.threatScore > 5 ||
                    this.bot.health <= 12 ||
                    criticalMobNames.has(t.name)
                ) &&
                t.name !== "low_health_no_food" &&
                t.name !== "starvation",
        );

        if (immediateThreats.length > 0) {
            const topThreat = immediateThreats[0]!;

            // If we're panicking or in cooldown, let the current escape plan play out
            if (this.isPanicking || Date.now() < this.panicCooldownUntil)
                return;

            this.isPanicking = true;
            this.panicCooldownUntil = Date.now() + 5000; // 5-second cooldown

            log.warn(
                "SENSOR: Critical Threat Detected. Interrupting TS execution loop.",
                {
                    cause: topThreat.name,
                    health: Math.round(this.bot.health),
                    distance: Math.round(topThreat.distance),
                },
            );

            this.config.onPanicStart?.(topThreat.name);
            this.config.onInterrupt(`panic_${topThreat.name}`);
        } else if (this.isPanicking && Date.now() > this.panicCooldownUntil) {
            // Coast is clear and cooldown finished. Drop the panic lock.
            this.isPanicking = false;
            log.info("SENSOR: Threat passed; releasing panic lock.");
            this.config.onPanicEnd?.("safe");
        }
    }
}
