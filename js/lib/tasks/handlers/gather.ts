import type { TaskContext } from "../registry.js";
import { escapeTree, waitForMs } from "../task.js";
import pkg from "mineflayer-pathfinder";

const { goals } = pkg;

// All overworld log types the bot can gather. When the LLM asks for
// "oak_log" but only birch trees exist nearby, we fall back through
// this list so we never timeout on the wrong biome.
const LOG_TYPES = [
    "oak_log",
    "birch_log",
    "spruce_log",
    "acacia_log",
    "jungle_log",
    "dark_oak_log",
    "mangrove_log",
    "cherry_log",
];

export async function handleGather(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts } = ctx;
    await escapeTree(bot, signal);

    if (signal.aborted) throw new Error("aborted");

    const requestedTarget = decision.target?.name;
    if (!requestedTarget) throw new Error("missing gather target");

    // Build the candidate list. If the LLM requested a specific log type,
    // try it first; then fall back to any other log so we don't loop on
    // the wrong biome. For non-log targets (stone, coal, etc.) just use
    // the exact name with no fallback.
    const isLogRequest =
        requestedTarget.endsWith("_log") || requestedTarget === "wood";

    const candidates: string[] = isLogRequest
        ? [requestedTarget, ...LOG_TYPES.filter((l) => l !== requestedTarget)]
        : [requestedTarget];

    // Find the nearest available block from the candidate list.
    let block: any = null;
    let resolvedTarget = requestedTarget;

    for (const name of candidates) {
        const found = bot.findBlock({
            maxDistance: 64,
            matching: (b: any) => b?.name === name,
        });
        if (found) {
            block = found;
            resolvedTarget = name;
            break;
        }
    }

    if (!block) {
        throw new Error(
            `no block found near bot for target "${requestedTarget}" (tried ${candidates.length} types)`,
        );
    }

    if (signal.aborted) throw new Error("aborted");

    // pathfinder.goto() is a Promise that resolves on goal_reached and
    // rejects on noPath / timeout. It is far more reliable than manually
    // listening to the "goal_reached" event, which can fire before the
    // listener is registered when the bot is already adjacent.
    const gotoTimeout = timeouts.gather ?? 30000;
    const gotoPromise = (bot as any).pathfinder.goto(
        new goals.GoalGetToBlock(
            block.position.x,
            block.position.y,
            block.position.z,
        ),
    );

    // Race pathfinder.goto() against the abort signal and a hard timeout.
    const abortPromise = new Promise<never>((_, reject) => {
        if (signal.aborted) {
            reject(new Error("aborted"));
            return;
        }
        signal.addEventListener("abort", () => reject(new Error("aborted")), {
            once: true,
        });
    });

    const timeoutPromise = new Promise<never>((_, reject) =>
        setTimeout(
            () => reject(new Error(`timeout navigating to ${resolvedTarget}`)),
            gotoTimeout,
        ),
    );

    await Promise.race([gotoPromise, abortPromise, timeoutPromise]);

    if (signal.aborted) throw new Error("aborted");

    // Re-fetch after movement — the block reference can go stale if a
    // chunk update changed the block while we walked.
    const freshBlock = bot.blockAt(block.position);
    if (!freshBlock || freshBlock.name !== resolvedTarget) {
        throw new Error(
            `block gone or changed after navigation: ${resolvedTarget}`,
        );
    }

    const tool = (bot as any).pathfinder.bestHarvestTool(freshBlock);
    if (tool != null) await bot.equip(tool, "hand");

    await bot.dig(freshBlock);
    await waitForMs(500, signal);
}
