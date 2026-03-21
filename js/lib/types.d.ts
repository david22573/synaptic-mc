import { Pathfinder } from "mineflayer-pathfinder";

declare module "mineflayer" {
    interface Bot {
        pathfinder: Pathfinder;
    }
}
