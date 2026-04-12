import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";
import { Runtime } from "../../control/runtime.js";
import { navigateWithFallbacks } from "../../movement/navigator.js";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";

const { goals } = pkg;

const BUILDING_BLOCKS = new Set([
    "dirt",
    "cobblestone",
    "oak_planks",
    "birch_planks",
    "spruce_planks",
    "stone",
    "netherrack",
]);

interface BlueprintNode {
    dx: number;
    dy: number;
    dz: number;
}

interface BuildContext extends StateContext {
    targetStructure: string;
    blueprint: BlueprintNode[];
    buildOrigin: Vec3 | null;
    materialType: number | null;
    currentIndex: number;
    stopMovement: () => void;
}

const SCHEMATICS: Record<string, BlueprintNode[]> = {
    shelter: [
        { dx: -1, dy: 0, dz: -1 },
        { dx: 0, dy: 0, dz: -1 },
        { dx: 1, dy: 0, dz: -1 },
        { dx: -1, dy: 0, dz: 0 },
        { dx: 1, dy: 0, dz: 0 },
        { dx: -1, dy: 0, dz: 1 },
        { dx: 1, dy: 0, dz: 1 },
        { dx: -1, dy: 1, dz: -1 },
        { dx: 0, dy: 1, dz: -1 },
        { dx: 1, dy: 1, dz: -1 },
        { dx: -1, dy: 1, dz: 0 },
        { dx: 1, dy: 1, dz: 0 },
        { dx: -1, dy: 1, dz: 1 },
        { dx: 1, dy: 1, dz: 1 },
        { dx: -1, dy: 2, dz: -1 },
        { dx: 0, dy: 2, dz: -1 },
        { dx: 1, dy: 2, dz: -1 },
        { dx: -1, dy: 2, dz: 0 },
        { dx: 0, dy: 2, dz: 0 },
        { dx: 1, dy: 2, dz: 0 },
        { dx: -1, dy: 2, dz: 1 },
        { dx: 0, dy: 2, dz: 1 },
        { dx: 1, dy: 2, dz: 1 },
    ],
    pillar: [
        { dx: 0, dy: -1, dz: 0 },
        { dx: 0, dy: 0, dz: 0 },
        { dx: 0, dy: 1, dz: 0 },
        { dx: 0, dy: 2, dz: 0 },
    ],
};

class ConstructState implements FSMState {
    name = "CONSTRUCTING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as BuildContext;
        const bot = sCtx.bot;
        const origin = sCtx.buildOrigin!;

        if (!sCtx.materialType) {
            sCtx.result = { status: "FAILED", reason: "MISSING_INGREDIENTS" };
            return null;
        }

        let consecutiveFailures = 0;
        while (sCtx.currentIndex < sCtx.blueprint.length) {
            if (sCtx.signal.aborted) throw new Error("aborted");
            if (consecutiveFailures > 5)
                throw new Error("PANIC: Bot is hopelessly stuck.");

            const node = sCtx.blueprint[sCtx.currentIndex];
            if (!node) break;

            const targetPos = origin.offset(node.dx, node.dy, node.dz);
            const targetBlock = bot.blockAt(targetPos);
            if (targetBlock && targetBlock.boundingBox === "block") {
                sCtx.currentIndex++;
                consecutiveFailures = 0;
                continue;
            }

            const hasMaterial = bot.inventory
                .items()
                .some((i: any) => i.type === sCtx.materialType);
            if (!hasMaterial) {
                sCtx.result = {
                    status: "FAILED",
                    reason: "MISSING_INGREDIENTS",
                };
                return null;
            }

            await bot.equip(sCtx.materialType, "hand");
            const refBlock = this.findReferenceBlock(bot, targetPos);

            if (!refBlock) {
                try {
                    bot.clearControlStates();
                    await navigateWithFallbacks(
                        bot,
                        new goals.GoalNear(
                            targetPos.x,
                            targetPos.y,
                            targetPos.z,
                            2,
                        ),
                        {
                            signal: sCtx.signal,
                            timeoutMs: 10000,
                            stopMovement: sCtx.stopMovement,
                            maxRetries: 2,
                        },
                    );
                } catch (e: any) {
                    if (e.message === "aborted") throw e;
                    consecutiveFailures++;
                }
                const retryRef = this.findReferenceBlock(bot, targetPos);
                if (!retryRef) {
                    sCtx.currentIndex++;
                    consecutiveFailures++;
                    continue;
                }
            }

            const distToBot = bot.entity.position.distanceTo(targetPos);
            if (distToBot < 1.5) {
                if (node.dx === 0 && node.dz === 0 && node.dy >= 0) {
                    const localRef = this.findReferenceBlock(bot, targetPos);
                    if (localRef) {
                        bot.setControlState("jump", true);
                        await bot.waitForTicks(2);
                        try {
                            const faceVector = targetPos.minus(
                                localRef.position,
                            );
                            await bot.placeBlock(localRef, faceVector);
                            consecutiveFailures = 0;
                        } catch (err) {
                            consecutiveFailures++;
                        } finally {
                            bot.setControlState("jump", false);
                        }
                    } else {
                        consecutiveFailures++;
                    }
                    sCtx.currentIndex++;
                    continue;
                } else {
                    try {
                        bot.clearControlStates();
                        await navigateWithFallbacks(
                            bot,
                            new goals.GoalNear(
                                targetPos.x,
                                targetPos.y,
                                targetPos.z,
                                2,
                            ),
                            {
                                signal: sCtx.signal,
                                timeoutMs: 5000,
                                stopMovement: sCtx.stopMovement,
                                maxRetries: 2,
                            },
                        );
                    } catch (e: any) {
                        if (e.message === "aborted") throw e;
                    }
                }
            }

            try {
                const finalRef = this.findReferenceBlock(bot, targetPos);
                if (finalRef) {
                    const faceVector = targetPos.minus(finalRef.position);
                    await bot.placeBlock(finalRef, faceVector);
                    consecutiveFailures = 0;
                } else {
                    consecutiveFailures++;
                }
            } catch (err: any) {
                consecutiveFailures++;
            }
            sCtx.currentIndex++;
        }
        sCtx.result = { status: "SUCCESS", reason: "BUILD_COMPLETE" };
        return null;
    }

    private findReferenceBlock(bot: any, pos: Vec3): any | null {
        const offsets = [
            new Vec3(0, -1, 0),
            new Vec3(1, 0, 0),
            new Vec3(-1, 0, 0),
            new Vec3(0, 0, 1),
            new Vec3(0, 0, -1),
            new Vec3(0, 1, 0),
        ];
        for (const offset of offsets) {
            const adjPos = pos.plus(offset);
            const block = bot.blockAt(adjPos);
            if (block && block.boundingBox === "block") return block;
        }
        return null;
    }
}

class PrepareBuildState implements FSMState {
    name = "PREPARING";
    async enter() {}
    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as BuildContext;
        const blueprint = SCHEMATICS[sCtx.targetStructure];
        if (!blueprint) {
            sCtx.result = {
                status: "FAILED",
                reason: `UNKNOWN_RECIPE: ${sCtx.targetStructure}`,
            };
            return null;
        }
        sCtx.blueprint = blueprint;
        let selectedMaterial: any = null;
        let totalBlocks = 0;
        for (const item of sCtx.bot.inventory.items()) {
            if (BUILDING_BLOCKS.has(item.name)) {
                totalBlocks += item.count;
                if (!selectedMaterial) selectedMaterial = item;
            }
        }
        if (!selectedMaterial || totalBlocks < blueprint.length * 0.5) {
            sCtx.result = { status: "FAILED", reason: "MISSING_INGREDIENTS" };
            return null;
        }
        sCtx.materialType = selectedMaterial.type;
        sCtx.buildOrigin = sCtx.bot.entity.position.floored();
        return new ConstructState();
    }
}

export async function handleBuild(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);
    const targetName = intent.target?.name?.toLowerCase() || "shelter";
    const fsmCtx: BuildContext = {
        bot,
        targetStructure: targetName,
        blueprint: [],
        buildOrigin: null,
        materialType: null,
        currentIndex: 0,
        targetEntity: null,
        searchRadius: 0,
        timeoutMs: timeouts.build ?? 60000,
        startTime: 0,
        signal,
        stopMovement,
        targetName: "",
    };

    const fsm = new StateMachineRunner(new PrepareBuildState(), fsmCtx);
    const runtime = new Runtime(bot);

    const result = await runtime.execute(fsm.run(), signal);

    if (result.status === "FAILED")
        throw new Error(result.reason || "unknown_fsm_failure");
}
