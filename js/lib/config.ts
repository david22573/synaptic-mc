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
    smelt: 30000,
    farm: 40000,
    mine: 45000,
    retreat: 15000,
    sleep: 20000,
    mark_location: 5000,
    recall_location: 5000,
    idle: 3000,
};

export const THREAT_WEIGHTS: Record<string, number> = {
    warden: 1000,
    creeper: 50,
    skeleton: 20,
    zombie: 10,
    spider: 10,
};

export interface Config {
    ws_url: string;
    viewer_port: number;
    enable_viewer: boolean;
    debug_chat: boolean;
    task_timeouts: Record<string, number>;
    threat_weights: Record<string, number>;
}

let configCache: Config | null = null;

export async function loadConfig(): Promise<Config> {
    if (configCache) return configCache;

    let timeoutId: NodeJS.Timeout | null = null;

    try {
        const controller = new AbortController();
        timeoutId = setTimeout(() => controller.abort(), 3000);

        const res = await fetch("http://127.0.0.1:8080/api/config", {
            signal: controller.signal,
        });

        if (!res.ok) throw new Error(`Config fetch failed: ${res.status}`);

        configCache = await res.json();

        if (configCache!.task_timeouts) {
            Object.assign(TASK_TIMEOUTS, configCache!.task_timeouts);
        }
        if (configCache!.threat_weights) {
            Object.assign(THREAT_WEIGHTS, configCache!.threat_weights);
        }

        configCache!.task_timeouts = TASK_TIMEOUTS;
        configCache!.threat_weights = THREAT_WEIGHTS;

        return configCache!;
    } catch (err) {
        console.warn(
            "Failed to fetch dynamic config, relying on static defaults",
            err instanceof Error ? err.message : String(err),
        );

        configCache = {
            ws_url: WS_URL,
            viewer_port: VIEWER_PORT,
            enable_viewer: ENABLE_VIEWER,
            debug_chat: DEBUG_CHAT,
            task_timeouts: TASK_TIMEOUTS,
            threat_weights: THREAT_WEIGHTS,
        };

        return configCache;
    } finally {
        if (timeoutId) clearTimeout(timeoutId);
    }
}
