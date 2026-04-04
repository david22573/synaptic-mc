// js/lib/movement/progress.ts
import { Vec3 } from "vec3";
import type { Bot } from "mineflayer";

export class ProgressTracker {
    private startPos: Vec3;
    private lastPos: Vec3;
    private startDistance: number;
    private target: Vec3;

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

    public checkStuck(bot: Bot): boolean {
        if (!bot.entity) return false;
        const dist = bot.entity.position.distanceTo(this.lastPos);
        this.lastPos = bot.entity.position.clone();
        return dist < 0.5; // Hasn't moved half a block
    }
}
