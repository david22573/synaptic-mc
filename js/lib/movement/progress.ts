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

    private lastYaw: number = 0;
    private yawOscillationCount: number = 0;

    /**
     * Checks if the bot is stuck by comparing current position, velocity, and yaw oscillation.
     */
    public checkStuck(bot: Bot): boolean {
        if (!bot.entity) return false;

        const currentPos = bot.entity.position;
        const dist = currentPos.distanceTo(this.lastPos);
        this.lastPos = currentPos.clone();

        const vel = bot.entity.velocity;
        const speed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);

        const currentYaw = bot.entity.yaw;
        const yawDelta = Math.abs(currentYaw - this.lastYaw);
        this.lastYaw = currentYaw;

        // Detect yaw oscillation (rapidly looking back and forth - often happens when stuck)
        if (yawDelta > 0.5 && speed < 0.05) {
            this.yawOscillationCount++;
        } else {
            this.yawOscillationCount = Math.max(0, this.yawOscillationCount - 1);
        }

        if ((dist < 0.1 && speed < 0.02) || this.yawOscillationCount > 10) {
            this.stuckStrikes++;
        } else {
            this.stuckStrikes = Math.max(0, this.stuckStrikes - 1);
        }

        return this.stuckStrikes >= 5;
    }
}
