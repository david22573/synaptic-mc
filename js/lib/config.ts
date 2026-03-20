import * as dotenv from "dotenv";
import * as fs from "fs";
import * as path from "path";

function loadEnv(): string {
    const candidates = [
        path.resolve(process.cwd(), ".env"),
        path.resolve(process.cwd(), "../.env"),
    ];

    for (const envPath of candidates) {
        if (fs.existsSync(envPath)) {
            dotenv.config({ path: envPath });
            return envPath;
        }
    }

    dotenv.config();
    return "default-dotenv-lookup";
}

export const ENV_PATH = loadEnv();
export const ENABLE_VIEWER = process.env.ENABLE_VIEWER === "true";
export const VIEWER_PORT = parseInt(process.env.VIEWER_PORT || "3000", 10);
export const DEBUG_CHAT = process.env.DEBUG_CHAT === "true";
export const WS_URL = process.env.WS_URL || "ws://localhost:8080/ws";

export const TASK_TIMEOUTS: Record<string, number> = {
    gather: 30000,
    craft: 20000,
    hunt: 30000,
    explore: 20000,
    build: 20000,
    retreat: 15000,
    idle: 3000,
};

export const THREAT_WEIGHTS: Record<string, number> = {
    warden: 1000,
    creeper: 100,
    skeleton: 20,
    zombie: 10,
    spider: 10,
};
