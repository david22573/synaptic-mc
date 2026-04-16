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
import { CircuitBreaker } from "../utils/circuit_breaker.js";

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

    // Kinematic Steering (Roadmap Item 5)
    kinematic?: {
        desiredHeading?: number; // radians
        desiredSpeed?: number;   // 0.0 - 1.0
        avoidanceVector?: Vec3;  // vector to add to heading
        lookTarget?: Vec3;       // precision look
    };
    
    pathfindingGoal: any | null;
}

export class BotController {
    public activeIntent: ActionIntent | null = null;
    public intentState: Record<string, any> = {};
    private loop: ControlLoop;
    private combat: CombatController;
    private pathfinder: RollingPathfinder;
    private intentStartedAt: number = 0;
    private minHoldDuration: number = 0;
    private canPreempt: boolean = true;
    private progress: ProgressTracker | null = null;
    private breaker: CircuitBreaker;

    constructor(
        public bot: Bot,
        private getThreats: () => ThreatInfo[] = () => [],
    ) {
        this.loop = new ControlLoop(this);
        this.combat = new CombatController(bot);
        this.pathfinder = new RollingPathfinder(bot);
        this.breaker = new CircuitBreaker();
    }

    public setIntent(intent: ActionIntent) {
        const now = Date.now();

        // Phase 1: Stop Action Thrashing (Intent Lease System)
        if (now - this.intentStartedAt < this.minHoldDuration) {
            // Cannot preempt during MinHold unless it's a higher priority survival action
            // In TS, we treat retreat and random_walk as absolute overrides
            if (intent.action !== 'retreat' && intent.action !== 'random_walk') {
                log.warn(`[Reflex] Ignoring intent swap to '${intent.action}' (MinHold active)`);
                return;
            }
        }

        // Check preemption lock
        if (!this.canPreempt && (now - this.intentStartedAt < 10000)) { // Hard 10s cap on non-preemptable tasks
            if (intent.action !== 'retreat' && intent.action !== 'random_walk') {
                log.warn(`[Reflex] Ignoring intent swap to '${intent.action}' (Non-preemptable active)`);
                return;
            }
        }

        if (this.activeIntent) {
            log.info(`Preempting intent '${this.activeIntent.action}' -> '${intent.action}'`);
            this.intentState.preempted = true;
            this.intentState.reason = "preempted";
        }

        log.info(`Swapping intent -> ${intent.action}`);
        this.activeIntent = intent;
        this.intentState = { completed: false, failed: false, preempted: false, reason: "" };
        this.intentStartedAt = now;
        
        // Default lease rules
        this.minHoldDuration = 0;
        this.canPreempt = true;

        // Specialized rules for survival actions
        if (intent.action === 'retreat' || intent.action === 'random_walk') {
            this.minHoldDuration = 2000;
            this.canPreempt = false;
        }

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

    public async adjust(perception: Perception): Promise<ActionPlan> {
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
            if (now - this.intentStartedAt > 2000) {
                // If not already in a survival intent, force one
                if (perception.intent?.action !== 'retreat' && perception.intent?.action !== 'random_walk') {
                    log.warn("[Reflex] Survival critical: triggering mechanical disengage");
                    this.intentStartedAt = now;
                    this.minHoldDuration = 2000;
                    this.canPreempt = false;
                }

                const nearest = closeThreats.sort((a, b) => a.distance - b.distance)[0];
                if (nearest && nearest.position && this.bot.entity) {
                    const dx = this.bot.entity.position.x - nearest.position.x;
                    const dz = this.bot.entity.position.z - nearest.position.z;
                    const yaw = Math.atan2(dx, dz);
                    this.bot.look(yaw, 0, true).catch(() => {});
                }
            }
        }

        // Mechanical Escape Override (Phase 1/5)
        if (now - this.intentStartedAt < this.minHoldDuration && !this.canPreempt) {
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
                if (this.breaker.canExecute()) {
                    log.warn("[Stuck] Bot detected as stuck. Applying recovery ladder.");
                    this.breaker.recordFailure();
                    
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
                } else {
                    // Circuit breaker open: don't spam recovery, try a simple random walk fallback
                    plan.controls.forward = true;
                    plan.controls.left = Math.random() > 0.5;
                    plan.controls.right = !plan.controls.left;
                    plan.clearPathfinder = true;
                }
            } else {
                this.breaker.recordSuccess();
            }
        }

        // 3. Handle Kinematic Steering (Roadmap Item 5)
        if (plan.kinematic) {
            const k = plan.kinematic;
            if (k.desiredHeading !== undefined) {
                let finalHeading = k.desiredHeading;
                if (k.avoidanceVector) {
                    const avoidanceYaw = Math.atan2(k.avoidanceVector.x, k.avoidanceVector.z);
                    finalHeading = (finalHeading + avoidanceYaw) / 2; // Simple blend
                }
                
                // Set yaw
                this.bot.look(finalHeading, this.bot.entity.pitch, true).catch(() => {});
                
                if (k.desiredSpeed && k.desiredSpeed > 0) {
                    plan.controls.forward = true;
                    plan.controls.sprint = k.desiredSpeed > 0.5;
                }
            }
            
            if (k.lookTarget) {
                plan.lookAt = k.lookTarget;
            }
        }

        // 4. Handle Continuous Pathing (Roadmap Item 7)
        if (plan.pathfindingGoal) {
            // Update rolling path
            const result = await this.pathfinder.updatePath(plan.pathfindingGoal);
            if (result.status === "noPath") {
                log.warn("[Reflex] Path calculation failed. Reporting failure to decision layer.");
                this.intentState.failed = true;
                this.intentState.reason = "no_path";
            }
            
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
