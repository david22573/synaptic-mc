// js/lib/control/runtime.ts
import { Bot } from "mineflayer";
import { applyWorldMovementReflexes, senseWorld } from "../utils/world.js";

export class Runtime {
    private active = true;

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
            this.readStateAndAdjust();
            
            const isMoving = (this.bot as any).pathfinder?.isMoving();
            // Poll faster when moving (100ms), slower when stationary (250ms)
            await this.bot.waitForTicks(isMoving ? 2 : 5);
        }

        return await taskPromise;
    }

    private readStateAndAdjust() {
        if (!this.bot.entity) return;

        const isMoving = (this.bot as any).pathfinder?.isMoving();
        const world = senseWorld(this.bot);

        if (!isMoving) {
            this.bot.setControlState("jump", false);
            return;
        }

        applyWorldMovementReflexes(this.bot, world);

        if (world.movement.steepDropAhead && !world.movement.inWater) {
            this.bot.setControlState("jump", false);
            return;
        }

        // Dynamic mid-task correction: Unstuck logic
        // Cast to any to bypass missing TS definitions for physics properties
        const collided = (this.bot.entity as any).isCollidedHorizontally;
        const vel = this.bot.entity.velocity;

        if (
            world.movement.inWater ||
            world.movement.submerged ||
            world.movement.inLava ||
            world.movement.inCobweb ||
            world.movement.inBerryBush ||
            world.movement.frontBlocked ||
            collided ||
            (Math.abs(vel.x) < 0.01 && Math.abs(vel.z) < 0.01)
        ) {
            this.bot.setControlState("jump", true);
        } else if (this.bot.entity.onGround) {
            this.bot.setControlState("jump", false);
        }
    }
}
