// js/lib/combat/controller.ts
import type { Bot } from "mineflayer";
import { log } from "../logger.js";

export class CombatController {
    constructor(private bot: Bot) {}

    public async disengage(): Promise<void> {
        log.info("[Combat] Disengaging from combat");
        this.bot.clearControlStates();
        
        if (!this.bot.entity) return;

        // Find nearest threat
        const threats = Object.values(this.bot.entities).filter(e => 
            e.type === 'mob' && e.position.distanceTo(this.bot.entity.position) < 10
        );

        if (threats.length > 0) {
            const nearest = threats.sort((a, b) => 
                a.position.distanceTo(this.bot.entity.position) - b.position.distanceTo(this.bot.entity.position)
            )[0];
            
            if (nearest) {
                // Look away from threat
                const dx = this.bot.entity.position.x - nearest.position.x;
                const dz = this.bot.entity.position.z - nearest.position.z;
                const yaw = Math.atan2(dx, dz);
                await this.bot.look(yaw, 0, true);
            }
        }

        this.bot.setControlState('forward', true);
        this.bot.setControlState('sprint', true);
        this.bot.setControlState('jump', true);

        await new Promise(r => setTimeout(r, 2000));
        this.bot.clearControlStates();
    }

    public async hitAndRun(target: any): Promise<void> {
        if (!target) return;
        
        await this.bot.lookAt(target.position, true);
        this.bot.attack(target);
        
        // Reflexive backoff after hit
        this.bot.setControlState('back', true);
        this.bot.setControlState('jump', true);
        await new Promise(r => setTimeout(r, 500));
        this.bot.setControlState('back', false);
        this.bot.setControlState('jump', false);
    }
}
