import {
    type FSMState,
    type StateContext,
    StateMachineRunner,
} from "../fsm.js";
import { type TaskContext } from "../registry.js";
import { escapeTree, moveToGoal } from "../utils.js";
import pkg from "mineflayer-pathfinder";
import { Vec3 } from "vec3";

const { goals } = pkg;

// Blocks we consider valid for cheap structural building
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

// Hardcoded schematics relative to the bot's feet (0,0,0)
const SCHEMATICS: Record<string, BlueprintNode[]> = {
    shelter: [
        // Ring 1 (feet level) - leaving a hole at (1, 0, 0) for a door
        { dx: -1, dy: 0, dz: -1 },
        { dx: 0, dy: 0, dz: -1 },
        { dx: 1, dy: 0, dz: -1 },
        { dx: -1, dy: 0, dz: 0 } /* DOOR HERE */,
        { dx: -1, dy: 0, dz: 1 },
        { dx: 0, dy: 0, dz: 1 },
        { dx: 1, dy: 0, dz: 1 },

        // Ring 2 (head level)
        { dx: -1, dy: 1, dz: -1 },
        { dx: 0, dy: 1, dz: -1 },
        { dx: 1, dy: 1, dz: -1 },
        { dx: -1, dy: 1, dz: 0 } /* DOOR TOP HERE */,
        { dx: -1, dy: 1, dz: 1 },
        { dx: 0, dy: 1, dz: 1 },
        { dx: 1, dy: 1, dz: 1 },

        // Roof (just covering the 3x3)
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
            sCtx.result = {
                status: "FAILED",
                reason: "MISSING_INGREDIENTS: Ran out of building materials.",
            };
            return null;
        }

        while (sCtx.currentIndex < sCtx.blueprint.length) {
            if (sCtx.signal.aborted) throw new Error("aborted");

            const node = sCtx.blueprint[sCtx.currentIndex];
            if (!node) break;
            const targetPos = origin.offset(node.dx, node.dy, node.dz);
            const targetBlock = bot.blockAt(targetPos);

            // Skip if it's already a solid block
            if (targetBlock && targetBlock.boundingBox === "block") {
                sCtx.currentIndex++;
                continue;
            }

            // Make sure we have the block equipped
            await bot.equip(sCtx.materialType, "hand");

            // Find a reference block to place against
            const refBlock = this.findReferenceBlock(bot, targetPos);

            if (!refBlock) {
                // If we can't find a reference, it's floating. Pathfind near it and try again next tick.
                try {
                    await moveToGoal(
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
                            dynamic: false,
                        },
                    );
                } catch (e) {}

                // Let's not get permanently stuck. If we still can't place it, skip it.
                const retryRef = this.findReferenceBlock(bot, targetPos);
                if (!retryRef) {
                    sCtx.currentIndex++;
                    continue;
                }
            }

            // Before placing, check if we need to move out of the way or jump
            const distToBot = bot.entity.position.distanceTo(targetPos);
            if (distToBot < 1.5) {
                if (node.dx === 0 && node.dz === 0 && node.dy >= 0) {
                    // It's trying to build inside itself (pillar jumping)
                    bot.setControlState("jump", true);
                    await new Promise((r) => setTimeout(r, 250));
                    bot.setControlState("jump", false);
                } else {
                    // Back up slightly so we don't clip into the block we are placing
                    await moveToGoal(
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
                            dynamic: false,
                        },
                    );
                }
            }

            try {
                const finalRef = this.findReferenceBlock(bot, targetPos);
                if (finalRef) {
                    const faceVector = targetPos.minus(finalRef.position);
                    await bot.placeBlock(finalRef, faceVector);
                }
            } catch (err) {
                // Placement can fail due to angle/hitbox, just continue to next block
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
            if (block && block.boundingBox === "block") {
                return block;
            }
        }
        return null;
    }
}

class PrepareBuildState implements FSMState {
    name = "PREPARING";

    async enter() {}

    async execute(ctx: StateContext): Promise<FSMState | null> {
        const sCtx = ctx as BuildContext;
        const bot = sCtx.bot;

        // 1. Validate structure type
        const blueprint = SCHEMATICS[sCtx.targetStructure];
        if (!blueprint) {
            sCtx.result = {
                status: "FAILED",
                reason: `UNKNOWN_RECIPE: No schematic found for ${sCtx.targetStructure}`,
            };
            return null;
        }
        sCtx.blueprint = blueprint;

        // 2. Find suitable building materials in inventory
        let selectedMaterial: any = null;
        let totalBlocks = 0;

        for (const item of bot.inventory.items()) {
            if (BUILDING_BLOCKS.has(item.name)) {
                totalBlocks += item.count;
                if (!selectedMaterial) {
                    selectedMaterial = item;
                }
            }
        }

        if (!selectedMaterial || totalBlocks < blueprint.length * 0.5) {
            // Allow trying even if slightly short
            sCtx.result = {
                status: "FAILED",
                reason: "MISSING_INGREDIENTS: Not enough building blocks (need dirt/cobble/planks).",
            };
            return null;
        }

        sCtx.materialType = selectedMaterial.type;
        sCtx.buildOrigin = bot.entity.position.floored();

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
        timeoutMs: timeouts.build ?? 60000, // Building takes time
        startTime: 0,
        signal,
        stopMovement,
        targetName: "",
    };

    const fsm = new StateMachineRunner(new PrepareBuildState(), fsmCtx);
    const result = await fsm.run();

    if (result.status === "FAILED") {
        throw new Error(result.reason || "unknown_fsm_failure");
    }
}
