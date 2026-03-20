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
        });

        this.ws.on("message", (data: Buffer) => {
            try {
                const msg = JSON.parse(data.toString());

                if (msg.type === "command") {
                    this.callbacks.onCommand(
                        msg.payload as models.IncomingDecision,
                    );
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
            log.error("Disconnected from Control Plane. Retrying in 5s...");
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
        this.reconnectTimer = setTimeout(() => {
            this.reconnectTimer = null;
            this.connect();
        }, 5000);
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
