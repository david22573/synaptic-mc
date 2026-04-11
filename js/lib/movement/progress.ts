// js/lib/movement/progress.ts
import { Vec3 } from "vec3";
import type { Bot } from "mineflayer";

export class ProgressTracker {
    private startPos: Vec3;
    private lastPos: Vec3;
    private startDistance: number;
    private target: Vec3;
    private stuckStrikes: number = 0;

    constructor(bot: Bot, target: Vec3) {
        this.startPos = bot.entity.position.clone();
        this.lastPos = this.startPos;
        this.target = target;
        this.startDistance = this.startPos.distanceTo(target);
    }

    public getProgress(bot: Bot): number {
        if (this.startDistance === 0) return 1;
        const currentDistance = bot.entity.position.distanceTo(this.target);
        return Math.max(0, 1 - currentDistance / this.startDistance);
    }

    public getDistance(bot: Bot): number {
        return bot.entity.position.distanceTo(this.target);
    }

    /**
     * Checks if the bot is stuck by comparing current position and actual velocity.
     * Uses a strike-based system to avoid false positives during legitimate slow movement
     * (e.g., soul sand, cobwebs, or jumping up blocks).
     */
    public checkStuck(bot: Bot): boolean {
        if (!bot.entity) return false;

        const currentPos = bot.entity.position;
        const dist = currentPos.distanceTo(this.lastPos);
        this.lastPos = currentPos.clone();

        const vel = bot.entity.velocity;
        const speed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);

        // If positional diff is tiny AND actual per-tick velocity is dead
        if (dist < 0.2 && speed < 0.02) {
            this.stuckStrikes++;
        } else {
            // Decaying strikes allows for intermittent slow movement without immediately
            // resetting the stuck state if the bot is still struggling.
            this.stuckStrikes = Math.max(0, this.stuckStrikes - 1);
        }

        return this.stuckStrikes >= 3;
    }
}
