import { AgentController } from "./agent-controller";
import { TaskCommitment } from "./task-commitment";
import { Prefetcher } from "./prefetcher";

export const botStore = $state({
    gameState: null as any,
    events: [] as any[],
    objective: "Initializing...",
    connectionStatus: "connecting" as
        | "connecting"
        | "connected"
        | "disconnected",
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
const maxEvents = 50;

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

            switch (message.type) {
                case "state_update":
                    botStore.gameState = message.payload;
                    controller.onStateUpdate(message.payload);
                    break;
                case "event_stream": {
                    const newEvent = {
                        ...message.payload,
                        timestamp: new Date().toLocaleTimeString(),
                    };
                    botStore.events = [newEvent, ...botStore.events].slice(
                        0,
                        maxEvents,
                    );

                    if (
                        message.payload.event === "task_start" &&
                        message.payload.task
                    ) {
                        if (commitment.shouldCommit(message.payload.task)) {
                            prefetcher.onTaskStart(message.payload.task);
                        }
                    }

                    if (message.payload.event === "task_end") {
                        commitment.reset();
                    }
                    break;
                }
                case "objective_update":
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

export function clearEventLog() {
    botStore.events = [];
}

export function disconnectBot() {
    if (ws) ws.close();
    if (reconnectTimer) clearTimeout(reconnectTimer);
}
