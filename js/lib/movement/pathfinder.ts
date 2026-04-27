import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import pkg from "mineflayer-pathfinder";
import { log } from "../logger.js";

const { goals } = pkg;

export interface PathResult {
    path: Vec3[];
    status: "success" | "noPath" | "timeout";
}

export class RollingPathfinder {
    private currentPath: Vec3[] = [];
    private lastPathTime = 0;
    private isCalculating = false;

    constructor(private bot: Bot) {}

    public getPath(): Vec3[] {
        return this.currentPath;
    }

    public async updatePath(goal: any): Promise<PathResult> {
        if (this.isCalculating) {
            return { path: this.currentPath, status: "success" };
        }

        // Limit repathing frequency to save CPU
        const now = Date.now();
        if (now - this.lastPathTime < 500 && this.currentPath.length > 0) {
            return { path: this.currentPath, status: "success" };
        }

        this.isCalculating = true;
        try {
            // We use the pathfinder directly to get the raw path points
            const results = (this.bot.pathfinder as any).getPathTo(this.bot.pathfinder.movements, goal);
            if (results && results.path) {
                this.currentPath = results.path.map((p: any) => new Vec3(p.x, p.y, p.z));
                this.lastPathTime = Date.now();
                return { path: this.currentPath, status: "success" };
            }
            return { path: [], status: "noPath" };
        } catch (err) {
            log.error("[Pathfinder] Path calculation failed", { error: err });
            return { path: [], status: "noPath" };
        } finally {
            this.isCalculating = false;
        }
    }

    public getNextWaypoint(lookahead: number = 3): Vec3 | null {
        if (this.currentPath.length === 0) return null;
        
        // Find the index in the path closest to current bot position
        const botPos = this.bot.entity.position;
        let closestIdx = 0;
        let minDist = Infinity;
        
        for (let i = 0; i < Math.min(this.currentPath.length, 10); i++) {
            const dist = botPos.distanceTo(this.currentPath[i]);
            if (dist < minDist) {
                minDist = dist;
                closestIdx = i;
            }
        }

        // Return a point slightly ahead for smoothness
        const targetIdx = Math.min(closestIdx + lookahead, this.currentPath.length - 1);
        return this.currentPath[targetIdx];
    }
}
