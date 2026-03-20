const DEBUG = process.env.DEBUG === "true";

interface Logger {
    info: (msg: string, meta?: Record<string, unknown>) => void;
    warn: (msg: string, meta?: Record<string, unknown>) => void;
    error: (msg: string, meta?: Record<string, unknown>) => void;
    debug: (msg: string, meta?: Record<string, unknown>) => void;
}

export const log: Logger = {
    info: (msg, meta = {}) =>
        console.log(
            JSON.stringify({
                level: "INFO",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),

    warn: (msg, meta = {}) =>
        console.warn(
            JSON.stringify({
                level: "WARN",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),

    error: (msg, meta = {}) =>
        console.error(
            JSON.stringify({
                level: "ERROR",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        ),

    debug: (msg, meta = {}) => {
        if (!DEBUG) return;
        console.log(
            JSON.stringify({
                level: "DEBUG",
                msg,
                ...meta,
                timestamp: new Date().toISOString(),
            }),
        );
    },
};
