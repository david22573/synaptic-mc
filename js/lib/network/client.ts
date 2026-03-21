import WebSocket from "ws";
import { log } from "../logger.js";
import * as models from "../models.js";

export interface ControlPlaneEvents {
    onCommand: (decision: models.IncomingDecision) => void;
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

    public connect(): void {
        if (this.ws) {
            this.ws.removeAllListeners();
            this.ws.close();
        }

        this.ws = new WebSocket(this.url);

        this.ws.on("open", () => {
            log.info("Connected to Go Control Plane", { ws_url: this.url });
            this.reconnectAttempts = 0;
        });

        this.ws.on("message", (data: Buffer) => {
            try {
                const msg = JSON.parse(data.toString());

                if (msg.type === "command") {
                    const decision = msg.payload as models.IncomingDecision;

                    if (!decision || !decision.action) {
                        log.error("Received malformed command payload", {
                            payload: msg.payload,
                        });
                        this.callbacks.onUnlock();
                        return;
                    }

                    if (!msg.trace || !msg.trace.trace_id) {
                        log.debug("Command missing trace context", {
                            action: decision.action,
                        });
                    }

                    decision.trace = msg.trace || {
                        trace_id: "unknown",
                        action_id: decision.id,
                    };

                    this.callbacks.onCommand(decision);
                    return;
                }

                if (msg.type === "planning_error" || msg.type === "noop") {
                    log.debug("Control plane unlocked bot", {
                        type: msg.type,
                        payload: msg.payload,
                    });
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
            log.error(
                "Disconnected from Control Plane. Initiating reconnect...",
            );
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

        this.ws.send(
            JSON.stringify({
                type: "event",
                payload: {
                    event,
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
        this.ws.send(JSON.stringify({ type: "state", payload: state }));
    }
}
