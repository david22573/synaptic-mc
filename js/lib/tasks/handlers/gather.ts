import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";

export async function handleGather(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal } = ctx;
    await escapeTree(bot, signal);

    const targetName = intent.target.name;
    const count = intent.count || 1;

    const blockId = bot.registry.blocksByName[targetName]?.id;
    if (blockId === undefined) {
        throw new Error(`UNKNOWN_BLOCK: ${targetName}`);
    }

    // Locate slightly more blocks than needed in case some are unreachable
    const blockPositions = bot.findBlocks({
        matching: blockId,
        maxDistance: 64,
        count: count + 5,
    });

    if (blockPositions.length === 0) {
        throw new Error(`NO_${targetName.toUpperCase()}_NEARBY`);
    }

    const targets = blockPositions
        .map((pos) => bot.blockAt(pos))
        .filter((b) => b !== null);

    // Ensure the high-level plugin aborts cleanly if a higher-priority task preempts it
    const onAbort = () => {
        // @ts-ignore - plugin method attached at runtime
        if (bot.collectBlock) bot.collectBlock.cancelTask();
    };
    signal.addEventListener("abort", onAbort, { once: true });

    try {
        // @ts-ignore - collect expects an array of block objects
        await bot.collectBlock.collect(targets.slice(0, count));
    } catch (err: any) {
        throw new Error(`COLLECT_FAILED: ${err.message}`);
    } finally {
        signal.removeEventListener("abort", onAbort);
    }
}
