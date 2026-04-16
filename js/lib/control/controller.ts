// js/lib/control/controller.ts
import { Bot } from "mineflayer";
import { ActionIntent, ThreatInfo } from "../models.js";
import { ControlLoop } from "./loop.js";
import { INTENT_EVALUATORS } from "../tasks/registry.js";
import { log } from "../logger.js";
import { Vec3 } from "vec3";
import { CombatController } from "../combat/controller.js";
import {
    getWorldControlOverrides,
    senseWorld,
    type WorldAwareness,
} from "../utils/world.js";

export interface Perception {
    pos: Vec3;
    health: number;
    intent: ActionIntent | null;
    state: Record<string, any>;
    threats: ThreatInfo[];
    world: WorldAwareness;
}

export interface ActionPlan {
    controls: Record<string, boolean>;
    lookAt: Vec3 | null;
    interact: "attack" | "use" | null;
    interactTarget: any | null;
    clearPathfinder: boolean;
}

export class BotController {
    public activeIntent: ActionIntent | null = null;
    public intentState: Record<string, any> = {};
    private loop: ControlLoop;
    private combat: CombatController;

    constructor(
        public bot: Bot,
        private getThreats: () => ThreatInfo[] = () => [],
    ) {
        this.loop = new ControlLoop(this);
        this.combat = new CombatController(bot);
    }

    public setIntent(intent: ActionIntent) {
        log.info(`Swapping intent -> ${intent.action}`);
        this.activeIntent = intent;
        this.intentState = { completed: false, failed: false, reason: "" };
        this.bot.clearControlStates();
        this.bot.pathfinder.setGoal(null);
    }

    public sense(): Perception {
        const threats = this.getThreats();
        return {
            pos: this.bot.entity?.position || new Vec3(0, 0, 0),
            health: this.bot.health,
            intent: this.activeIntent,
            state: this.intentState,
            threats,
            world: senseWorld(this.bot, threats),
        };
    }

    public adjust(perception: Perception): ActionPlan {
        const defaultPlan: ActionPlan = {
            controls: {
                forward: false,
                back: false,
                left: false,
                right: false,
                jump: false,
                sprint: false,
            },
            lookAt: null,
            interact: null,
            interactTarget: null,
            clearPathfinder: false,
        };

        // Combat disengage reflex
        const closeThreats = perception.threats.filter(t => t.distance < 5);
        if ((this.bot.health < 10 || closeThreats.length >= 3) && perception.intent?.action !== 'retreat') {
            log.warn("[Reflex] Survival critical: triggering combat disengage");
            this.combat.disengage(); // Fire and forget async
            return this.mergeWorldReflexes(defaultPlan, perception.world);
        }

        if (!perception.intent || !this.bot.entity) {
            return this.mergeWorldReflexes(defaultPlan, perception.world);
        }

        const evaluator = INTENT_EVALUATORS[perception.intent.action];
        if (!evaluator) {
            log.warn(
                `No continuous evaluator found for intent: ${perception.intent.action}`,
            );
            return this.mergeWorldReflexes(defaultPlan, perception.world);
        }

        const plan = evaluator(this.bot, perception, defaultPlan);
        return this.mergeWorldReflexes(plan, perception.world);
    }

    public async act(plan: ActionPlan) {
        if (plan.clearPathfinder && this.bot.pathfinder.goal) {
            this.bot.pathfinder.setGoal(null);
        }

        // Sync mechanical inputs
        for (const [control, state] of Object.entries(plan.controls)) {
            this.bot.setControlState(control as any, state);
        }

        // Continuous fluid aiming (Fire and forget, don't block the tick)
        if (plan.lookAt) {
            this.bot.lookAt(plan.lookAt, true).catch(() => {});
        }

        // Interaction frame
        if (plan.interact === "attack" && plan.interactTarget) {
            this.bot.attack(plan.interactTarget);
        } else if (plan.interact === "use" && plan.interactTarget) {
            this.bot.activateEntity(plan.interactTarget);
        }
    }

    public start() {
        this.loop.start();
    }
    public stop() {
        this.loop.stop();
    }

    private mergeWorldReflexes(
        plan: ActionPlan,
        world: WorldAwareness,
    ): ActionPlan {
        const overrides = getWorldControlOverrides(this.bot, world);
        if (
            Object.keys(overrides.controls).length === 0 &&
            !overrides.clearPathfinder &&
            !overrides.lookAt
        ) {
            return plan;
        }

        const baseControls = overrides.urgent
            ? {
                  forward: false,
                  back: false,
                  left: false,
                  right: false,
                  jump: false,
                  sprint: false,
              }
            : plan.controls;

        return {
            ...plan,
            controls: {
                ...baseControls,
                ...overrides.controls,
            },
            lookAt: overrides.lookAt || plan.lookAt,
            clearPathfinder: plan.clearPathfinder || overrides.clearPathfinder,
        };
    }
}
