import type { Bot } from "mineflayer";
import type { Entity } from "prismarine-entity";
import { type MoveOptions } from "./utils.js";
export declare function gotoEntity(bot: Bot, entity: Entity, range: number, opts: MoveOptions): Promise<boolean>;
export declare function attackEntity(bot: Bot, entity: Entity): Promise<void>;
export declare function findNearestEntity(bot: Bot, name: string, radius: number): Entity | null;
//# sourceMappingURL=primitives.d.ts.map