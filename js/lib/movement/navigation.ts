import type { Bot } from "mineflayer";
import { Vec3 } from "vec3";
import { log } from "../logger.js";

export interface SteerOptions {
    lookahead?: number;
    smoothness?: number;
    antiSpin?: boolean;
}

export class Navigation {
    private lastYaw = 0;
    private lastJumpTime = 0;

    constructor(private bot: Bot) {}

    public steer(target: Vec3, opts: SteerOptions = {}): Record<string, boolean> {
        const { smoothness = 0.3, antiSpin = true } = opts;
        const controls = {
            forward: false,
            back: false,
            left: false,
            right: false,
            jump: false,
            sprint: false,
        };

        if (!this.bot.entity) return controls;

        const pos = this.bot.entity.position;
        const dx = target.x - pos.x;
        const dz = target.z - pos.z;
        const dy = target.y - pos.y;

        let desiredYaw = Math.atan2(-dx, -dz);
        const dist2d = Math.sqrt(dx * dx + dz * dz);
        const desiredPitch = Math.atan2(dy, dist2d);

        // Anti-Spin / Anti-Jitter logic
        if (antiSpin) {
            let yawDiff = desiredYaw - this.lastYaw;
            while (yawDiff < -Math.PI) yawDiff += Math.PI * 2;
            while (yawDiff > Math.PI) yawDiff -= Math.PI * 2;

            // Clamp rotation speed for human-like movement
            const maxRotation = 0.5; // rad per tick
            if (Math.abs(yawDiff) > maxRotation) {
                desiredYaw = this.lastYaw + Math.sign(yawDiff) * maxRotation;
            }
        }

        // Path Smoothing: interpolate look
        const smoothedYaw = this.lastYaw * (1 - smoothness) + desiredYaw * smoothness;
        this.bot.look(smoothedYaw, desiredPitch, true);
        this.lastYaw = smoothedYaw;

        controls.forward = true;
        controls.sprint = dist2d > 2.0;

        // Jump Timing & Obstacle Prediction
        const vel = this.bot.entity.velocity;
        const horizontalSpeed = Math.sqrt(vel.x * vel.x + vel.z * vel.z);

        const yaw = this.bot.entity.yaw;
        const blockInFront = this.bot.blockAt(pos.offset(-Math.sin(yaw) * 1.5, 0, -Math.cos(yaw) * 1.5));
        const blockAbove = this.bot.blockAt(pos.offset(-Math.sin(yaw) * 1.5, 1, -Math.cos(yaw) * 1.5));

        const now = Date.now();
        if (
            blockInFront && blockInFront.boundingBox === "block" &&
            (!blockAbove || blockAbove.name === "air") &&
            now - this.lastJumpTime > 500
        ) {
            controls.jump = true;
            this.lastJumpTime = now;
        }

        return controls;
    }
}
