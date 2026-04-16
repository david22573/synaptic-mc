// js/lib/control/runtime.ts
import { Bot } from "mineflayer";
import { applyWorldMovementReflexes, senseWorld } from "../utils/world.js";
import { autoEat, emergencyFlee, preventFall, unstuckLogic } from "../movement/recovery.js";

export class Runtime {
    private active = true;
    private lastJumpAt = 0;
    private lastEatAt = 0;

    constructor(private bot: Bot) {}

    /**
     * Executes a task concurrently with a read/adjust movement loop.
     */
    async execute<T>(taskPromise: Promise<T>, signal: AbortSignal): Promise<T> {
        this.active = true;

        taskPromise
            .finally(() => {
                this.active = false;
            })
            .catch(() => {});

        while (this.active && !signal.aborted) {
            await this.readStateAndAdjust();
            
            const isMoving = (this.bot as any).pathfinder?.isMoving();
            // Poll faster when moving (100ms), slower when stationary (250ms)
            await this.bot.waitForTicks(isMoving ? 2 : 5);
        }

        return await taskPromise;
    }

    private async readStateAndAdjust() {
        if (!this.bot.entity) return;

        const isMoving = (this.bot as any).pathfinder?.isMoving();
        const world = senseWorld(this.bot);

        // 1. Instant Reactions (Highest ROI)
        
        // Lava detection interrupt
        if (world.panicCause === 'lava' || world.panicCause === 'fire') {
            await emergencyFlee(this.bot, 2000);
            return;
        }

        // Fall prevention
        preventFall(this.bot);

        // Auto Eat
        if (this.bot.food < 14 && Date.now() - this.lastEatAt > 5000) {
            this.lastEatAt = Date.now();
            await autoEat(this.bot);
        }

        if (!isMoving) {
            this.bot.setControlState("jump", false);
            return;
        }

        // Only let world sensor take over locomotion for immediate danger.
        if (world.panicCause && world.escapeTarget) {
            applyWorldMovementReflexes(this.bot, world);
        }

        const controls = (this.bot as any).controlState || {};
        const collided = (this.bot.entity as any).isCollidedHorizontally;
        const vel = this.bot.entity.velocity;
        const horizontalSpeed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);
        const wantsMotion =
            Boolean(
                controls.forward ||
                    controls.back ||
                    controls.left ||
                    controls.right,
            ) || isMoving;

        // Unstuck Logic
        if (wantsMotion && horizontalSpeed < 0.01 && collided) {
            await unstuckLogic(this.bot);
        }

        const shouldJump =
            world.movement.inWater ||
            world.movement.submerged ||
            world.movement.inLava ||
            world.movement.inCobweb ||
            world.movement.inBerryBush ||
            ((collided || world.movement.frontBlocked) &&
                wantsMotion &&
                horizontalSpeed < 0.03);

        const canPulseJump =
            this.bot.entity.onGround ||
            world.movement.inWater ||
            world.movement.submerged ||
            world.movement.inLava;

        if (
            shouldJump &&
            canPulseJump &&
            Date.now() - this.lastJumpAt >= 400
        ) {
            this.lastJumpAt = Date.now();
            this.bot.setControlState("jump", true);
        } else if (!shouldJump || this.bot.entity.onGround) {
            this.bot.setControlState("jump", false);
        }
    }
}
