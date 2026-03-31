import WebSocket from "ws";
import { EventEmitter } from "events";

interface ClientConfig {
    url: string;
    reconnectIntervalMs: number;
    maxReconnectIntervalMs: number;
    pingIntervalMs: number;
}

export class SynapticClient extends EventEmitter {
    private ws: WebSocket | null = null;
    private config: ClientConfig;
    private reconnectAttempts = 0;
    private isIntentionallyClosed = false;
    private pingTimeout: NodeJS.Timeout | null = null;
    private reconnectTimer: NodeJS.Timeout | null = null;

    constructor(config: Partial<ClientConfig> = {}) {
        super();
        this.config = {
            url: config.url || "ws://127.0.0.1:8080/bot/ws",
            reconnectIntervalMs: config.reconnectIntervalMs || 1000,
            maxReconnectIntervalMs: config.maxReconnectIntervalMs || 15000,
            pingIntervalMs: config.pingIntervalMs || 30000,
        };
    }

    public connect(): void {
        if (this.isIntentionallyClosed) return;

        if (
            this.ws &&
            (this.ws.readyState === WebSocket.OPEN ||
                this.ws.readyState === WebSocket.CONNECTING)
        ) {
            return;
        }

        console.log(`[Network] Connecting to ${this.config.url}...`);

        try {
            this.ws = new WebSocket(this.config.url);
        } catch (err) {
            console.error("[Network] Failed to instantiate WebSocket:", err);
            this.scheduleReconnect();
            return;
        }

        this.ws.on("open", () => {
            console.log("[Network] WebSocket connected.");
            this.emit("connected");
            this.heartbeat();

            // Stabilize backoff: only reset if the connection survives for 5 seconds
            setTimeout(() => {
                if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                    this.reconnectAttempts = 0;
                }
            }, 5000);
        });

        this.ws.on("ping", () => {
            this.heartbeat();
        });

        this.ws.on("message", (data: WebSocket.Data) => {
            try {
                const msg = JSON.parse(data.toString());
                this.emit("message", msg);

                if (msg.type === "DISPATCH_TASK") {
                    this.emit("dispatch", msg.payload);
                } else if (msg.type === "ABORT_TASK") {
                    this.emit("abort", msg.payload);
                }
            } catch (err) {
                console.error("[Network] Failed to parse message:", err);
            }
        });

        this.ws.on("close", () => {
            this.clearHeartbeat();
            this.ws = null;
            this.emit("disconnected");
            if (!this.isIntentionallyClosed) {
                this.scheduleReconnect();
            }
        });

        this.ws.on("error", (err: any) => {
            if (!this.isIntentionallyClosed) {
                console.error(
                    "[Network] WebSocket error:",
                    err?.message || err,
                );
            }
            this.ws?.close();
        });
    }

    private heartbeat(): void {
        this.clearHeartbeat();
        this.pingTimeout = setTimeout(() => {
            if (this.isIntentionallyClosed) return;
            console.warn("[Network] Heartbeat timeout. Closing connection.");
            this.ws?.terminate();
        }, this.config.pingIntervalMs + 5000);
    }

    private clearHeartbeat(): void {
        if (this.pingTimeout) {
            clearTimeout(this.pingTimeout);
            this.pingTimeout = null;
        }
    }

    private scheduleReconnect(): void {
        if (this.isIntentionallyClosed) return;

        if (this.reconnectTimer) clearTimeout(this.reconnectTimer);

        const backoff = Math.min(
            this.config.reconnectIntervalMs *
                Math.pow(1.5, this.reconnectAttempts),
            this.config.maxReconnectIntervalMs,
        );
        this.reconnectAttempts++;
        console.log(`[Network] Reconnecting in ${Math.round(backoff)}ms...`);
        this.reconnectTimer = setTimeout(() => this.connect(), backoff);
    }

    public disconnect(): void {
        this.isIntentionallyClosed = true;
        this.clearHeartbeat();
        if (this.reconnectTimer) {
            clearTimeout(this.reconnectTimer);
            this.reconnectTimer = null;
        }
        if (this.ws) {
            this.ws.close();
            this.ws = null;
        }
    }

    public sendState(state: any): void {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        this.ws.send(JSON.stringify({ type: "STATE_UPDATE", payload: state }));
    }

    // In SynapticClient class, replace the sendEvent overloads:

    public sendPanic(error: Error): void {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

        const payload = {
            error: error.message || "Unknown error",
            stack: error.stack || "No stack trace",
        };

        this.ws.send(
            JSON.stringify({
                type: "PANIC_TRIGGERED",
                payload: payload,
            }),
        );
    }

    public sendEvent(
        event: string,
        actionStr: string,
        commandId?: string,
        cause?: string,
        startTime?: number,
    ): void;
    public sendEvent(event: string, payload: Record<string, any>): void;
    public sendEvent(
        event: string,
        arg2: string | Record<string, any>,
        commandId = "",
        cause = "",
        startTime = 0,
    ): void {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;

        let msgType = "TASK_END";
        let payload: any;

        if (typeof arg2 === "object") {
            payload = arg2;
            // REMOVE the manual type mapping - let callers specify explicitly
        } else {
            // Original string-based API
            const actionStr = arg2 as string;
            const duration_ms = startTime > 0 ? Date.now() - startTime : 0;

            if (event === "task_start") msgType = "TASK_START";
            if (event === "death") msgType = "BOT_DEATH";
            if (event === "panic_retreat_start") msgType = "PANIC_TRIGGERED";
            if (event === "panic_retreat_end") msgType = "PANIC_RESOLVED";
            if (event === "task_aborted") msgType = "TASK_END";

            let status = event;
            if (event === "task_start") status = "STARTED";
            if (event === "task_completed") status = "COMPLETED";
            if (event === "task_failed") status = "FAILED";
            if (event === "task_aborted") status = "ABORTED";

            payload = {
                status: status,
                action: actionStr,
                command_id: commandId,
                cause: cause,
                duration_ms: duration_ms,
            };
        }

        this.ws.send(
            JSON.stringify({
                type: msgType,
                payload: payload,
            }),
        );
    }
}
