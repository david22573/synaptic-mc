import { type TaskContext } from "../registry.js";
import { collectBlocks, escapeTree } from "../utils.js";
import { Runtime } from "../../control/runtime.js";
import { NoTargetsNearbyError, isAbortError } from "../../errors.js";

export async function handleMine(ctx: TaskContext): Promise<void> {
    const { bot, intent, signal } = ctx;

    const taskLogic = async () => {
        await escapeTree(bot, signal);

        const targetName = intent.target.name;
        const count = intent.count || 1;

        try {
            await collectBlocks(bot, [targetName], count, signal);
        } catch (err: any) {
            if (err instanceof NoTargetsNearbyError) {
                throw new Error(`NO_${targetName.toUpperCase()}_NEARBY`);
            }
            throw err;
        }
    };

    await new Runtime(bot).execute(taskLogic(), signal);
}
