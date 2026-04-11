// js/lib/control/runtime.ts
import { Bot } from "mineflayer";

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
            await this.bot.waitForTicks(2);
        }

        return await taskPromise;
    }

    private readStateAndAdjust() {
        if (!this.bot.entity) return;

        const isMoving = this.bot.pathfinder?.isMoving();

        // Cast to any to bypass missing TS definitions for physics properties
        const collided = (this.bot.entity as any).isCollidedHorizontally;
        const vel = this.bot.entity.velocity;

        if (isMoving) {
            // Dynamic mid-task correction: Unstuck logic
            if (
                collided ||
                (Math.abs(vel.x) < 0.01 && Math.abs(vel.z) < 0.01)
            ) {
                this.bot.setControlState("jump", true);
            } else if (this.bot.entity.onGround) {
                this.bot.setControlState("jump", false);
            }
        } else {
            this.bot.setControlState("jump", false);
        }
    }
}
