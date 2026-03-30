import type { Bot } from "mineflayer";
import type { ControlPlaneClient } from "../network/client.js";
export interface SurvivalConfig {
    onInterrupt: (reason: string) => void;
    stopMovement: () => void;
}
export declare class SurvivalSystem {
    bot: Bot;
    private client;
    private config;
    private running;
    private isPanicking;
    private lastDangerAt;
    private lastFleeTime;
    private diggingEscape;
    private pillaringOut;
    constructor(bot: Bot, client: ControlPlaneClient, config: SurvivalConfig);
    start(): void;
    stop(): void;
    reset(): void;
    isPanickingNow(): boolean;
    private checkSurvival;
    private fleeTo;
    private digEscape;
    private pillarOut;
}
//# sourceMappingURL=survival.d.ts.map