import type { Bot } from "mineflayer";
import * as models from "../models.js";
export declare function runTask(bot: Bot, rawIntent: models.ActionIntent, signal: AbortSignal, timeouts: Record<string, number>, getThreats: () => models.ThreatInfo[], computeSafeRetreat: (threats: models.ThreatInfo[]) => {
    x: number;
    z: number;
}, stopMovement: () => void): Promise<void>;
//# sourceMappingURL=task.d.ts.map