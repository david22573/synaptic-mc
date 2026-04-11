import { type TaskContext } from "../registry.js";
import { escapeTree, LOG_BLOCK_NAMES } from "../utils.js";
import { Runtime } from "../../control/runtime.js";

function resolveGatherTargets(targetName: string): string[] {
    if (targetName === "log") {
        return [...LOG_BLOCK_NAMES];
    }

    if (
        LOG_BLOCK_NAMES.includes(targetName as (typeof LOG_BLOCK_NAMES)[number])
    ) {
        return [
            targetName,
            ...LOG_BLOCK_NAMES.filter((name) => name !== targetName),
        ];
    }

    return [targetName];
}

export async function handleGather(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal } = ctx;

    const taskLogic = async () => {
        await escapeTree(bot, signal);

        const targetName = intent.target.name.toLowerCase();
        const count = intent.count || 1;
        const candidateNames = resolveGatherTargets(targetName);

        const targets = [];
        for (const candidateName of candidateNames) {
            const blockId = bot.registry.blocksByName[candidateName]?.id;
            if (blockId === undefined) continue;

            const blockPositions = bot.findBlocks({
                matching: blockId,
                maxDistance: 64,
                count: count + 5,
            });

            const candidateTargets = blockPositions
                .map((pos) => bot.blockAt(pos))
                .filter((b) => b !== null);

            if (candidateTargets.length > 0) {
                targets.push(...candidateTargets);
            }

            if (targets.length >= count) break;
        }

        if (targets.length === 0) {
            throw new Error(`NO_${targetName.toUpperCase()}_NEARBY`);
        }

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
    };

    await new Runtime(bot).execute(taskLogic(), signal);
}
