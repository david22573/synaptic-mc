import type { TaskContext } from "../registry.js";
import {
    findNearestBlockByName,
    moveToGoal,
    escapeTree,
    waitForMs,
} from "../task.js";

export async function handleGather(ctx: TaskContext): Promise<void> {
    const { bot, decision, signal, timeouts, stopMovement } = ctx;
    await escapeTree(bot, signal);

    const target = decision.target?.name;
    if (!target) throw new Error("missing gather target");

    const block = findNearestBlockByName(bot, target);
    if (!block) throw new Error(`block not found: ${target}`);

    if (!block.position) throw new Error(`block position not found: ${target}`);

    await moveToGoal(
        bot,
        block.position,
        signal,
        timeouts.gather ?? 10000,
        stopMovement,
    );

    if (signal.aborted) throw new Error("aborted");

    const tool = bot.pathfinder.bestHarvestTool(block);
    if (tool !== undefined && tool !== null) await bot.equip(tool, "hand");

    await bot.dig(block);
    await waitForMs(500, signal);
}
