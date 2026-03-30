import { type TaskContext } from "../registry.js";
import { escapeTree } from "../utils.js";

export async function handleMine(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal } = ctx;
    await escapeTree(bot, signal);

    const targetName = intent.target.name;
    const count = intent.count || 1;

    const blockId = bot.registry.blocksByName[targetName]?.id;
    if (blockId === undefined) {
        throw new Error(`UNKNOWN_ORE: ${targetName}`);
    }

    // Look for a few extra blocks in case some are unreachable or fall in lava
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

    const onAbort = () => {
        // @ts-ignore
        if (bot.collectBlock) bot.collectBlock.cancelTask();
    };
    signal.addEventListener("abort", onAbort, { once: true });

    try {
        // @ts-ignore
        await bot.collectBlock.collect(targets.slice(0, count));
    } catch (err: any) {
        throw new Error(`COLLECT_FAILED: ${err.message}`);
    } finally {
        signal.removeEventListener("abort", onAbort);
    }
}
