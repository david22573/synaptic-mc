import { Bot } from "mineflayer";
import { ActionPlan, Perception } from "../../control/controller.js";
import { findNearestEntity } from "../primitives.js";

const WEAPON_SCORES: Record<string, number> = {
    netherite_sword: 10,
    diamond_sword: 9,
    iron_sword: 8,
    stone_sword: 7,
    wooden_sword: 6,
    golden_sword: 5,
    netherite_axe: 9.5,
    diamond_axe: 8.5,
    iron_axe: 7.5,
    stone_axe: 6.5,
    wooden_axe: 5.5,
    golden_axe: 4.5,
};

function ensureBestEquipment(bot: Bot, state: Record<string, unknown>) {
    if (Date.now() - (Number(state.lastEquipCheck) || 0) < 1000) return;
    state.lastEquipCheck = Date.now();

    let bestWeapon = null;
    let bestScore = -1;
    for (const item of bot.inventory.items()) {
        const score = WEAPON_SCORES[item.name] || 0;
        if (score > bestScore) {
            bestScore = score;
            bestWeapon = item;
        }
    }
    const currentWeapon = bot.heldItem;
    if (
        bestWeapon &&
        (!currentWeapon || (currentWeapon as any).type !== (bestWeapon as any).type)
    ) {
        bot.equip((bestWeapon as any).type, "hand").catch(() => {});
    }
    const offhand = bot.inventory.slots[45];
    if (!offhand || offhand.name !== "shield") {
        const shield = bot.inventory
            .items()
            .find((i) => i.name === "shield");
        if (shield) {
            bot.equip(shield.type, "off-hand").catch(() => {});
        }
    }
}

export function evaluateHunt(
    bot: Bot,
    perception: Perception,
    plan: ActionPlan,
): ActionPlan {
    const { intent, state, pos } = perception;
    const targetName = intent?.target?.name?.toLowerCase();
    const targetCount = intent?.count || 1;

    if (!targetName) {
        state.failed = true;
        state.reason = "No target specified";
        return plan;
    }

    if (!state.killCount) state.killCount = 0;

    if (state.killCount >= targetCount) {
        state.completed = true;
        plan.clearPathfinder = true;
        return plan;
    }

    ensureBestEquipment(bot, state);

    if (!state.targetId) {
        const target = findNearestEntity(bot, targetName, 48);
        if (!target) {
            state.failed = true;
            state.reason = `Could not find any ${targetName} nearby`;
            return plan;
        }
        state.targetId = target.id;
    }

    const target = bot.entities[state.targetId];
    const isValidTarget =
        target &&
        target.isValid &&
        (target.health === undefined || target.health > 0);

    if (!isValidTarget) {
        state.targetId = null;
        state.killCount++;
        return plan;
    }

    const dist = pos.distanceTo(target.position);
    plan.lookAt = target.position.offset(0, target.height * 0.8, 0);
    plan.clearPathfinder = true;

    const weaponCooldown = state.weaponCooldown || 0;
    const canAttack = Date.now() > weaponCooldown;

    // Advanced Kiting & Strafe Logic
    if (dist > 4.0) {
        plan.controls.forward = true;
        plan.controls.sprint = true;
    } else if (dist < 2.5) {
        plan.controls.back = true;
        plan.controls.forward = false;
        plan.controls.sprint = false;
    }

    // Circle Strafe
    if (dist <= 5.0) {
        if (!state.strafeDir || Math.random() < 0.05) {
            state.strafeDir = Math.random() > 0.5 ? "left" : "right";
        }
        plan.controls[state.strafeDir as string] = true;
    }

    // Bunny hop for crits and dodging
    if (dist <= 4.0 && bot.entity.onGround && Math.random() < 0.2) {
        plan.controls.jump = true;
    }

    // Shield Logic
    const offhand = bot.inventory.slots[45];
    if (offhand && offhand.name === "shield") {
        const isTargetAttacking = (target.metadata[14] as any) === 1; // Basic animation check
        if (isTargetAttacking || (dist < 3.0 && !canAttack)) {
            bot.activateItem(true);
            state.shieldActive = true;
        } else {
            bot.deactivateItem();
            state.shieldActive = false;
        }
    }

    if (dist <= 3.5 && canAttack) {
        // Critical hit timing
        const isFalling = bot.entity.velocity.y < -0.1;
        if (isFalling || !bot.entity.onGround) {
            plan.interact = "attack";
            plan.interactTarget = target;

            const heldItem = bot.heldItem;
            const cdTime = heldItem?.name.includes("axe") ? 1050 : 650;
            state.weaponCooldown = Date.now() + cdTime;
            
            // Momentarily drop shield to attack
            if (state.shieldActive) {
                bot.deactivateItem();
                setTimeout(() => {
                    if (state.shieldActive) bot.activateItem(true);
                }, 100);
            }
        }
    }

    return plan;
}
