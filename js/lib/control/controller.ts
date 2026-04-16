// js/lib/control/controller.ts
import { Bot } from "mineflayer";
import { ActionIntent, ThreatInfo } from "../models.js";
import { ControlLoop } from "./loop.js";
import { INTENT_EVALUATORS } from "../tasks/registry.js";
import { log } from "../logger.js";
import { Vec3 } from "vec3";
import { CombatController } from "../combat/controller.js";
import { RollingPathfinder } from "../movement/pathfinder.js";
import { steerTowards } from "../movement/navigator.js";
import { getWorldControlOverrides, senseWorld, type WorldAwareness } from "../utils/world.js";
import { ProgressTracker } from "../movement/progress.js";

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
    pathfindingGoal: any | null; // New field for continuous pathing
}

export class BotController {
    public activeIntent: ActionIntent | null = null;
    public intentState: Record<string, any> = {};
    private loop: ControlLoop;
    private combat: CombatController;
    private pathfinder: RollingPathfinder;
    private lastDisengageAt: number = 0;
    private progress: ProgressTracker | null = null;

    constructor(
        public bot: Bot,
        private getThreats: () => ThreatInfo[] = () => [],
    ) {
        this.loop = new ControlLoop(this);
        this.combat = new CombatController(bot);
        this.pathfinder = new RollingPathfinder(bot);
    }

    public setIntent(intent: ActionIntent) {
        const now = Date.now();

        if (now - this.lastDisengageAt < 2000) {
            if (intent.action !== 'retreat' && intent.action !== 'random_walk') {
                log.warn(`[Reflex] Ignoring intent swap to '${intent.action}' during active escape`);
                return;
            }
        }

        log.info(`Swapping intent -> ${intent.action}`);
        this.activeIntent = intent;
        this.intentState = { completed: false, failed: false, reason: "" };
        this.bot.clearControlStates();
        
        if (this.bot.pathfinder && this.bot.pathfinder.goal) {
            this.bot.pathfinder.setGoal(null);
        }
        this.progress = null; // Reset progress tracking for new intent
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
            pathfindingGoal: null,
        };

        const now = Date.now();
        const closeThreats = perception.threats.filter(t => t.distance < 5);

        // Survival Reflex
        if (this.bot.health < 10 || closeThreats.length >= 3) {
            if (now - this.lastDisengageAt > 2000) {
                this.lastDisengageAt = now;
                log.warn("[Reflex] Survival critical: triggering mechanical disengage");

                const nearest = closeThreats.sort((a, b) => a.distance - b.distance)[0];
                if (nearest && nearest.position && this.bot.entity) {
                    const dx = this.bot.entity.position.x - nearest.position.x;
                    const dz = this.bot.entity.position.z - nearest.position.z;
                    const yaw = Math.atan2(dx, dz);
                    this.bot.look(yaw, 0, true).catch(() => {});
                }
            }
        }

        if (now - this.lastDisengageAt < 2000) {
            defaultPlan.controls.forward = true;
            defaultPlan.controls.sprint = true;
            defaultPlan.controls.jump = true;
            defaultPlan.clearPathfinder = true;
            return this.mergeWorldReflexes(defaultPlan, perception.world);
        }

        if (!perception.intent || !this.bot.entity) {
            return this.mergeWorldReflexes(defaultPlan, perception.world);
        }

        const evaluator = INTENT_EVALUATORS[perception.intent.action];
        if (!evaluator) {
            return this.mergeWorldReflexes(defaultPlan, perception.world);
        }

        // 1. Evaluate Intent
        const plan = evaluator(this.bot, perception, defaultPlan);

        // 2. Proactive Stuck Detection (Roadmap Item 8)
        if (this.activeIntent && (plan.controls.forward || plan.pathfindingGoal)) {
            if (!this.progress) {
                this.progress = new ProgressTracker(this.bot, this.bot.entity.position.clone());
            }
            if (this.progress.checkStuck(this.bot)) {
                log.warn("[Stuck] Bot detected as stuck. Applying recovery ladder.");
                
                // Recovery Ladder
                if (Math.random() < 0.3) {
                    plan.controls.jump = true;
                } else if (Math.random() < 0.6) {
                    plan.controls.left = Math.random() > 0.5;
                    plan.controls.right = !plan.controls.left;
                } else {
                    // Force a repath by clearing current path
                    plan.clearPathfinder = true;
                }
            }
        }

        // 3. Handle Continuous Pathing (Roadmap Item 7)
        if (plan.pathfindingGoal) {
            // Update rolling path
            this.pathfinder.updatePath(plan.pathfindingGoal);
            const waypoint = this.pathfinder.getNextWaypoint();
            if (waypoint) {
                // Determine motor inputs toward waypoint
                const steering = steerTowards(this.bot, waypoint, 1.0, false);
                plan.controls = { ...plan.controls, ...steering };
            }
        }

        return this.mergeWorldReflexes(plan, perception.world);
    }

    public async act(plan: ActionPlan) {
        if (plan.clearPathfinder && this.bot.pathfinder && this.bot.pathfinder.goal) {
            this.bot.pathfinder.setGoal(null);
        }

        for (const [control, state] of Object.entries(plan.controls)) {
            this.bot.setControlState(control as any, state);
        }

        if (plan.lookAt) {
            this.bot.lookAt(plan.lookAt, true).catch(() => {});
        }

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
            pathfindingGoal: plan.pathfindingGoal,
        };
    }
}
