import { type Bot } from "mineflayer";
import * as models from "../models.js";
export declare function getThreats(bot: Bot): models.ThreatInfo[];
export declare function computeSafeRetreat(bot: Bot, threats: models.ThreatInfo[], distance?: number): {
    x: number;
    z: number;
};
//# sourceMappingURL=threats.d.ts.map