// js/lib/control/loop.ts
import { BotController } from "./controller.js";
import { log } from "../logger.js";

export class ControlLoop {
    private timer: NodeJS.Timeout | null = null;
    private isRunning = false;
    private tickRateMs = 50; // Native 20 TPS

    constructor(private controller: BotController) {}

    public start() {
        if (this.isRunning) return;
        this.isRunning = true;
        this.timer = setInterval(() => this.tick(), this.tickRateMs);
        log.info("Continuous control loop active at 20 TPS");
    }

    public stop() {
        if (this.timer) clearInterval(this.timer);
        this.isRunning = false;
        this.timer = null;
        log.info("Continuous control loop stopped");
    }

    private async tick() {
        try {
            // 1. SENSE: Gather environment telemetry
            const perception = this.controller.sense();

            // 2. ADJUST: Evaluate current intent to determine ideal physical state
            const plan = this.controller.adjust(perception);

            // 3. ACT: Apply calculated mechanical inputs
            await this.controller.act(plan);
        } catch (err) {
            log.error("Tick execution failed", { error: err });
        }
    }
}
