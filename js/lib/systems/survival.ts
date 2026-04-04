import type { Bot } from "mineflayer";
import { log } from "../logger.js";
import { getThreats } from "../utils/threats.js";

export interface SurvivalConfig {
    onInterrupt: (reason: string) => void;
    stopMovement: () => void;
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
                // Run twice a second. Fast enough to catch creepers, slow enough to spare the CPU.
                this.tickTimeout = setTimeout(tick, 500);
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

        // Filter for things that are actually trying to kill us right now
        const immediateThreats = threats.filter(
            (t: any) =>
                t.distance < 16 &&
                t.threatScore > 5 &&
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

            // Phase 0.1: Removed this.config.stopMovement() to allow movement during panic.

            // This aborts the current TS FSM with cause: "INTERRUPTED"
            // Go's FeedbackAnalyzer catches this, drops the micro-plan, and asks Strategy for a Retreat Directive.
            this.config.onInterrupt(`panic_${topThreat.name}`);
        } else if (this.isPanicking && Date.now() > this.panicCooldownUntil) {
            // Coast is clear and cooldown finished. Drop the panic lock.
            this.isPanicking = false;
            log.info("SENSOR: Threat passed; releasing panic lock.");
        }
    }
}
