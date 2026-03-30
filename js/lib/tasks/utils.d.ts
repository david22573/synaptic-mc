import type { Bot } from "mineflayer";
export declare function isOnSolidGround(bot: any): boolean;
export declare function isInFoliage(bot: any): boolean;
export declare function escapeTree(bot: any, signal: AbortSignal): Promise<void>;
export declare function waitForMs(ms: number, signal: AbortSignal): Promise<void>;
export interface MoveOptions {
    signal: AbortSignal;
    timeoutMs: number;
    stopMovement: () => void;
    dynamic?: boolean;
    stuckTimeoutMs?: number;
}
export declare function moveToGoal(bot: any, goal: any, opts: MoveOptions): Promise<void>;
export declare function findNearestBlockByName(bot: Bot, blockName: string): any;
export declare function makeRoomInInventory(bot: any, slotsNeeded?: number): Promise<void>;
export declare function placePortableUtility(bot: any, blockName: string): Promise<any>;
//# sourceMappingURL=utils.d.ts.map