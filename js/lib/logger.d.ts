import { type TraceContext } from "./models.js";
interface LogMeta extends Record<string, unknown> {
    trace?: TraceContext;
}
interface Logger {
    info: (msg: string, meta?: LogMeta) => void;
    warn: (msg: string, meta?: LogMeta) => void;
    error: (msg: string, meta?: LogMeta) => void;
    debug: (msg: string, meta?: LogMeta) => void;
}
export declare const log: Logger;
export {};
//# sourceMappingURL=logger.d.ts.map