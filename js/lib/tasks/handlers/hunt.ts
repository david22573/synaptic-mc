import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

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

interface CombatContext extends StateContext {
    targetName: string;
    targetCount: number;
    killCount: number;
    targetEntity: any | null;
    hasShield: boolean;
    isBlocking: boolean;
    lastAttackTime: number;
    stopMovement: () => void;
}

class EngageState implements FSMState {
    name = "ENGAGING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as CombatContext;
        const bot = sCtx.bot;
        const target = sCtx.targetEntity;

        const combatStartTime = Date.now();
        const maxCombatDurationMs = 60000;

        bot.pathfinder.setGoal(new goals.GoalFollow(target, 2), true);

        try {
            while (target && target.isValid && target.health > 0) {
                if (sCtx.signal.aborted) throw new Error("aborted");

                if (Date.now() - combatStartTime > maxCombatDurationMs) {
                    throw new Error(
                        "PANIC: Combat loop exceeded max duration. Target might be bugged or unreachable.",
                    );
                }

                await this.ensureBestEquipment(bot, sCtx);

                const dist = bot.entity.position.distanceTo(target.position);

                if (dist <= 3.5) {
                    await bot.lookAt(
                        target.position.offset(0, target.height * 0.8, 0),
                        true,
                    );

                    const weapon = bot.heldItem;
                    let cooldown = 650;
                    if (weapon && weapon.name.includes("axe")) cooldown = 1050;

                    const now = Date.now();
                    if (now - sCtx.lastAttackTime >= cooldown) {
                        if (sCtx.hasShield && sCtx.isBlocking) {
                            bot.deactivateItem();
                            sCtx.isBlocking = false;
                            await bot.waitForTicks(2);
                        }

                        bot.attack(target);
                        sCtx.lastAttackTime = Date.now();

                        if (sCtx.hasShield) {
                            bot.activateItem(true);
                            sCtx.isBlocking = true;
                        }
                    } else {
                        if (sCtx.hasShield && !sCtx.isBlocking) {
                            bot.activateItem(true);
                            sCtx.isBlocking = true;
                        }
                    }
                } else {
                    if (sCtx.hasShield && sCtx.isBlocking) {
                        bot.deactivateItem();
                        sCtx.isBlocking = false;
                    }
                }

                await bot.waitForTicks(1);
            }
        } catch (err: any) {
            if (err.message !== "aborted") {
                throw new Error(
                    `PANIC: Internal error during combat loop: ${err.stack || err.message}`,
                );
            }
            throw err;
        } finally {
            bot.pathfinder.setGoal(null);
            bot.clearControlStates();
            if (sCtx.hasShield && sCtx.isBlocking) {
                bot.deactivateItem();
                sCtx.isBlocking = false;
            }
        }

        if (!target.isValid || target.health <= 0) {
            sCtx.killCount++;

            const deathPos = target.position.clone();
            bot.pathfinder.setGoal(
                new goals.GoalNear(deathPos.x, deathPos.y, deathPos.z, 1),
            );
            await bot.waitForTicks(20);
            bot.pathfinder.setGoal(null);

            if (sCtx.killCount >= sCtx.targetCount) {
                sCtx.result = { status: "SUCCESS", reason: "HUNT_COMPLETE" };
                return null;
            }
        }

        return new SearchState();
    }

    private async ensureBestEquipment(bot: any, sCtx: CombatContext) {
        let bestWeapon: any = null;
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
            (!currentWeapon || currentWeapon.type !== bestWeapon.type)
        ) {
            try {
                await bot.equip(bestWeapon.type, "hand");
            } catch (e) {}
        }

        const offhand = bot.inventory.slots[45];
        if (!offhand || offhand.name !== "shield") {
            const shield = bot.inventory
                .items()
                .find((i: any) => i.name === "shield");
            if (shield) {
                try {
                    await bot.equip(shield.type, "off-hand");
                    sCtx.hasShield = true;
                } catch (e) {
                    sCtx.hasShield = false;
                }
            } else {
                sCtx.hasShield = false;
            }
        } else {
            sCtx.hasShield = true;
        }
    }
}

class SearchState implements FSMState {
    name = "SEARCHING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as CombatContext;
        const bot = sCtx.bot;

        const filter = (entity: any) =>
            entity.name === sCtx.targetName &&
            entity.type === "mob" &&
            entity.health > 0;

        const target = bot.nearestEntity(filter);

        if (!target) {
            sCtx.result = {
                status: "FAILED",
                reason: `NO_ENTITY: Could not find any ${sCtx.targetName} nearby.`,
            };
            return null;
        }

        sCtx.targetEntity = target;
        return new EngageState();
    }
}

class PrepareCombatState implements FSMState {
    name = "PREPARING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as CombatContext;
        const engager = new EngageState();
        await engager["ensureBestEquipment"](sCtx.bot, sCtx);
        return new SearchState();
    }
}

export async function handleHunt(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const targetName = intent.target?.name;
    if (!targetName) {
        throw new Error("MISSING_INGREDIENTS: No target specified for hunt");
    }

    const fsmCtx: CombatContext = {
        bot,
        targetName: targetName.toLowerCase(),
        targetCount: intent.count || 1,
        killCount: 0,
        targetEntity: null,
        hasShield: false,
        isBlocking: false,
        lastAttackTime: 0,
        searchRadius: 0,
        timeoutMs: timeouts.hunt ?? 120000,
        startTime: 0,
        signal,
        stopMovement,
    };

    const fsm = new StateMachineRunner(new PrepareCombatState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
