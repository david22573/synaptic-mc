import WebSocket from "ws";
import { log } from "../logger.js";
import * as models from "../models.js";

export interface ControlPlaneEvents {
    onCommand: (intent: models.ActionIntent) => void;
    onUnlock: () => void;
}

export class ControlPlaneClient {
    private ws: WebSocket | null = null;
    private readonly url: string;
    private readonly callbacks: ControlPlaneEvents;
    private reconnectTimer: NodeJS.Timeout | null = null;
    private reconnectAttempts: number = 0;

    constructor(url: string, callbacks: ControlPlaneEvents) {
        this.url = url;
        this.callbacks = callbacks;
    }

    public isConnected(): boolean {
        return this.ws !== null && this.ws.readyState === WebSocket.OPEN;
    }

    public connect(): void {
        if (this.ws) {
            this.ws.removeAllListeners();
            this.ws.terminate();
            this.ws = null;
        }

        this.ws = new WebSocket(this.url);

        this.ws.on("open", () => {
            log.info("Connected to Go Control Plane", { ws_url: this.url });
            this.reconnectAttempts = 0;
            if (this.reconnectTimer) {
                clearTimeout(this.reconnectTimer);
                this.reconnectTimer = null;
            }
        });

        this.ws.on("message", (data: Buffer) => {
            try {
                const msg = JSON.parse(data.toString());

                if (msg.type === "command") {
                    const intent = msg.payload as models.ActionIntent;

                    if (!intent || !intent.action) {
                        log.error("Received malformed intent payload", {
                            payload: msg.payload,
                        });
                        this.callbacks.onUnlock();
                        return;
                    }

                    // Ensure trace is populated for deterministic logging
                    intent.trace = msg.trace || {
                        trace_id: "unknown",
                        action_id: intent.id,
                    };

                    // Fallback to 1 if Go sent a zero-value count by mistake
                    if (!intent.count || intent.count <= 0) {
                        intent.count = 1;
                    }

                    this.callbacks.onCommand(intent);
                    return;
                }

                if (
                    msg.type === "planning_error" ||
                    msg.type === "noop" ||
                    msg.type === "abort_task"
                ) {
                    log.debug("Control plane unlocked bot", { type: msg.type });
                    this.callbacks.onUnlock();
                }
            } catch (err) {
                log.error("Failed to parse control-plane message", {
                    err: err instanceof Error ? err.message : String(err),
                });
                this.callbacks.onUnlock();
            }
        });

        this.ws.on("close", () => {
            log.error("Disconnected from Control Plane. Reconnecting...");
            this.callbacks.onUnlock();
            this.scheduleReconnect();
        });

        this.ws.on("error", (err) => {
            log.error("WebSocket error", {
                err: err instanceof Error ? err.message : String(err),
            });
        });
    }

    private scheduleReconnect(): void {
        if (this.reconnectTimer) return;

        const baseDelay = 2000;
        const maxDelay = 30000;
        let delay = baseDelay * Math.pow(2, this.reconnectAttempts);

        if (delay > maxDelay) delay = maxDelay;

        const jitter = delay * 0.2 * (Math.random() * 2 - 1);
        const finalDelay = Math.max(1000, delay + jitter);

        log.info("Scheduling reconnect", {
            delay_ms: Math.round(finalDelay),
            attempt: this.reconnectAttempts + 1,
        });

        this.reconnectTimer = setTimeout(() => {
            this.reconnectAttempts++;
            this.reconnectTimer = null;
            this.connect();
        }, finalDelay);
    }

    public sendEvent(
        event: string,
        actionStr: string,
        commandId = "",
        cause = "",
        startTime = 0,
    ): void {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

        const duration_ms = startTime > 0 ? Date.now() - startTime : 0;
        let msgType = "TASK_END";

        if (event === "death") msgType = "BOT_DEATH";
        if (event === "panic_retreat_start") msgType = "PANIC_TRIGGERED";
        if (event === "panic_retreat_end") msgType = "PANIC_RESOLVED";
        if (event === "task_aborted") msgType = "TASK_END";

        let status = event;
        if (event === "task_completed") status = "COMPLETED";
        if (event === "task_failed") status = "FAILED";
        if (event === "task_aborted") status = "ABORTED";

        this.ws.send(
            JSON.stringify({
                type: msgType,
                payload: {
                    status: status,
                    action: actionStr,
                    command_id: commandId,
                    cause,
                    duration_ms,
                },
            }),
        );
    }

    public sendState(state: Record<string, unknown>): void {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        this.ws.send(JSON.stringify({ type: "STATE_TICK", payload: state }));
    }
}
