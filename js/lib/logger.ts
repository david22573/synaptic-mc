import { type TraceContext } from "./models.js";

const DEBUG = process.env.DEBUG === "true";

interface LogMeta extends Record<string, unknown> {
    trace?: TraceContext;
}

interface Logger {
    info: (msg: string, meta?: LogMeta) => void;
    warn: (msg: string, meta?: LogMeta) => void;
    error: (msg: string, meta?: LogMeta) => void;
    debug: (msg: string, meta?: LogMeta) => void;
}

function formatLog(level: string, msg: string, meta: LogMeta = {}): string {
    const { trace, ...rest } = meta;
    return JSON.stringify({
        level,
        msg,
        trace_id: trace?.trace_id,
        action_id: trace?.action_id,
        milestone_id: trace?.milestone_id,
        ...rest,
        timestamp: new Date().toISOString(),
    });
}

export const log: Logger = {
    info: (msg, meta) => console.log(formatLog("INFO", msg, meta)),
    warn: (msg, meta) => console.warn(formatLog("WARN", msg, meta)),
    error: (msg, meta) => console.error(formatLog("ERROR", msg, meta)),
    debug: (msg, meta) => {
        if (!DEBUG) return;
        console.log(formatLog("DEBUG", msg, meta));
    },
};
