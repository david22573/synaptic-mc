import { type TaskContext } from "../registry.js";
import { collectBlocks, escapeTree, LOG_BLOCK_NAMES } from "../utils.js";
import { Runtime } from "../../control/runtime.js";
import { NoTargetsNearbyError, isAbortError } from "../../errors.js";

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

        try {
            await collectBlocks(bot, candidateNames, count, signal);
        } catch (err: any) {
            if (err instanceof NoTargetsNearbyError) {
                throw new Error(`NO_${targetName.toUpperCase()}_NEARBY`);
            }
            throw err;
        }
    };

    await new Runtime(bot).execute(taskLogic(), signal);
}
