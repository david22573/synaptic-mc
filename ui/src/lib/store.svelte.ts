// ui/src/lib/store.svelte.ts

import { AgentController } from "./agent-controller";
import { TaskCommitment } from "./task-commitment";
import { Prefetcher } from "./prefetcher";

const maxEvents = 50;
const EVENT_TTL_MS = 5 * 60 * 1000;

export const botStore = $state({
    gameState: null as any,
    events: [] as any[],
    objective: "Initializing...",
    connectionStatus: "connecting" as
        | "connecting"
        | "connected"
        | "disconnected",

    addEvent(event: any) {
        const now = Date.now();
        this.events = [
            { ...event, ingestTime: now },
            ...this.events.filter(
                (e) => now - (e.ingestTime || now) < EVENT_TTL_MS,
            ),
        ].slice(0, maxEvents);
    },
});

export const uiStore = $state({
    tooltip: "",
    mouseX: 0,
    mouseY: 0,
});

export const controller = new AgentController();
export const commitment = new TaskCommitment();
export const prefetcher = new Prefetcher();

let ws: WebSocket | null = null;
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
let reconnectAttempts = 0;

export function connectToBot() {
    botStore.connectionStatus = "connecting";
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const host = window.location.host;
    const wsUrl = `${protocol}//${host}/ui/ws`;

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
        botStore.connectionStatus = "connected";
        reconnectAttempts = 0;
        if (reconnectTimer) clearTimeout(reconnectTimer);
    };

    ws.onmessage = (event) => {
        try {
            const message = JSON.parse(event.data);
            if (!message.type || !message.payload) return;

            switch (message.type.toUpperCase()) {
                case "SYNC_REQUEST":
                    if (message.payload?.authoritative_state) {
                        controller.reconcileState(
                            message.payload.authoritative_state,
                        );
                    }
                    break;
                case "STATE_UPDATE":
                    botStore.gameState = message.payload;
                    controller.onStateUpdate(message.payload);
                    break;

                case "EVENT_STREAM": {
                    const goEvent = message.payload;

                    let innerPayload: any = {};
                    // FIX: Go broadcast payload uses lowercase 'payload'
                    const rawPayload = goEvent.payload || goEvent.Payload;

                    if (rawPayload) {
                        try {
                            const decoded = atob(rawPayload);
                            innerPayload = JSON.parse(decoded);
                        } catch {
                            try {
                                innerPayload =
                                    typeof rawPayload === "string"
                                        ? JSON.parse(rawPayload)
                                        : rawPayload;
                            } catch {
                                innerPayload = rawPayload;
                            }
                        }
                    }

                    const newEvent = {
                        // FIX: Go broadcast payload uses lowercase 'type'
                        type: goEvent.type || goEvent.Type || "UNKNOWN",
                        payload: innerPayload,
                        timestamp: new Date().toLocaleTimeString(),
                    };

                    botStore.addEvent(newEvent);

                    if (
                        newEvent.type === "TASK_START" &&
                        newEvent.payload.task
                    ) {
                        if (commitment.shouldCommit(newEvent.payload.task)) {
                            prefetcher.onTaskStart(newEvent.payload.task);
                        }
                    }
                    if (newEvent.type === "TASK_END") commitment.reset();
                    break;
                }
                case "OBJECTIVE_UPDATE":
                    botStore.objective = message.payload;
                    break;
            }
        } catch (err) {
            console.error("Failed to parse message:", err);
        }
    };

    ws.onclose = () => {
        botStore.connectionStatus = "disconnected";
        scheduleReconnect();
    };

    ws.onerror = () => {
        botStore.connectionStatus = "disconnected";
    };
}

function scheduleReconnect() {
    if (reconnectTimer) clearTimeout(reconnectTimer);
    const delay = Math.min(3000 * Math.pow(1.5, reconnectAttempts), 30000);
    reconnectAttempts++;
    reconnectTimer = setTimeout(() => {
        connectToBot();
    }, delay);
}

export function sendCameraControl(yaw: number, pitch: number) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;

    ws.send(
        JSON.stringify({
            type: "CONTROL_INPUT",
            payload: {
                action: "camera_move",
                yaw,
                pitch,
                timestamp: performance.now(),
            },
        }),
    );
}

export function clearEventLog() {
    botStore.events = [];
}

export function disconnectBot() {
    if (ws) ws.close();
    if (reconnectTimer) clearTimeout(reconnectTimer);
}
