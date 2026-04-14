// js/lib/systems/survival.ts
import type { Bot } from "mineflayer";
import { log } from "../logger.js";
import { getThreats } from "../utils/threats.js";
import { senseWorld } from "../utils/world.js";

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
        this.reset();
    }

    public reset() {
        this.running = false;
        this.isPanicking = false;
        this.panicCooldownUntil = 0;
        if (this.tickTimeout) {
            clearTimeout(this.tickTimeout);
            this.tickTimeout = null;
        }
    }

    public isPanickingNow(): boolean {
        return this.isPanicking;
    }

    private checkSurvival() {
        const threats = getThreats(this.bot);
        const world = senseWorld(this.bot, threats);

        const criticalMobNames = new Set(["creeper", "skeleton", "witch"]);

        // React earlier when health is already compromised or a high-risk mob is close.
        const immediateThreats = threats.filter(
            (t) =>
                t.distance < 20 &&
                (
                    (t.threatScore ?? 0) > 5 ||
                    this.bot.health <= 12 ||
                    criticalMobNames.has(t.name)
                ) &&
                t.name !== "low_health_no_food" &&
                t.name !== "starvation",
        );

        const panicCause =
            world.panicCause ||
            (immediateThreats.length > 0 ? immediateThreats[0]!.name : null);

        if (panicCause) {
            const topThreat = immediateThreats[0]!;

            // If we're panicking or in cooldown, let the current escape plan play out
            if (this.isPanicking || Date.now() < this.panicCooldownUntil)
                return;

            this.isPanicking = true;
            this.panicCooldownUntil =
                Date.now() +
                (world.panicCause === "lava" || world.panicCause === "drowning"
                    ? 8000
                    : 15000);

            log.warn(
                "SENSOR: Critical Threat Detected. Interrupting TS execution loop.",
                {
                    cause: panicCause,
                    health: Math.round(this.bot.health),
                    distance:
                        topThreat?.distance !== undefined
                            ? Math.round(topThreat.distance)
                            : 0,
                },
            );

            this.config.onPanicStart?.(panicCause);
            this.config.onInterrupt(`panic_${panicCause}`);
        } else if (
            this.isPanicking &&
            Date.now() > this.panicCooldownUntil &&
            !world.panicCause
        ) {
            // Coast is clear and cooldown finished. Drop the panic lock.
            this.isPanicking = false;
            log.info("SENSOR: Threat passed; releasing panic lock.");
            this.config.onPanicEnd?.("safe");
        }
    }
}
