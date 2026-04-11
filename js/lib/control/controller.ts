// js/lib/control/controller.ts
import { Bot } from "mineflayer";
import { ActionIntent, ThreatInfo } from "../models.js";
import { ControlLoop } from "./loop.js";
import { INTENT_EVALUATORS } from "../tasks/registry.js";
import { log } from "../logger.js";
import { Vec3 } from "vec3";

export interface Perception {
    pos: Vec3;
    health: number;
    intent: ActionIntent | null;
    state: Record<string, any>;
    threats: ThreatInfo[];
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

    constructor(
        public bot: Bot,
        private getThreats: () => ThreatInfo[] = () => [],
    ) {
        this.loop = new ControlLoop(this);
    }

    public setIntent(intent: ActionIntent) {
        log.info(`Swapping intent -> ${intent.action}`);
        this.activeIntent = intent;
        this.intentState = { completed: false, failed: false, reason: "" };
        this.bot.clearControlStates();
        this.bot.pathfinder.setGoal(null);
    }

    public sense(): Perception {
        return {
            pos: this.bot.entity?.position || new Vec3(0, 0, 0),
            health: this.bot.health,
            intent: this.activeIntent,
            state: this.intentState,
            threats: this.getThreats(),
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

        if (!perception.intent || !this.bot.entity) {
            return defaultPlan;
        }

        const evaluator = INTENT_EVALUATORS[perception.intent.action];
        if (!evaluator) {
            log.warn(
                `No continuous evaluator found for intent: ${perception.intent.action}`,
            );
            return defaultPlan;
        }

        return evaluator(this.bot, perception, defaultPlan);
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
}
