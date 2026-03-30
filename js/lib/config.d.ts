export declare const ENV_PATH: string;
export declare const ENABLE_VIEWER: boolean;
export declare const VIEWER_PORT: number;
export declare const DEBUG_CHAT: boolean;
export declare const WS_URL: string;
export declare const TASK_TIMEOUTS: Record<string, number>;
export declare const THREAT_WEIGHTS: Record<string, number>;
export interface Config {
    ws_url: string;
    viewer_port: number;
    enable_viewer: boolean;
    debug_chat: boolean;
    task_timeouts: Record<string, number>;
    threat_weights: Record<string, number>;
}
export declare function loadConfig(): Promise<Config>;
//# sourceMappingURL=config.d.ts.map