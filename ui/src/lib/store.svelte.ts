import { AgentController } from "./agent-controller";
import { Prefetcher } from "./prefetcher";
import { TaskCommitment } from "./task-commitment";
import type { GameState, Action, Target, Task } from "./models";

export type { GameState, Task };

export interface BotEvent {
    type: string;
    payload: any;
    timestamp: string;
}

// Global instances for logic management
export const controller = new AgentController();
const prefetcher = new Prefetcher();
const commitment = new TaskCommitment();

class BotStore {
    gameState = $state<GameState | null>(null);
    objective = $state<string>("Initializing...");
    events = $state<BotEvent[]>([]);
    connectionStatus = $state<"connected" | "connecting" | "disconnected">(
        "disconnected",
    );

    addEvent(event: BotEvent) {
        this.events = [event, ...this.events].slice(0, 100);
    }
}

class UIStore {
    tooltip = $state<string | null>(null);
    mouseX = $state(0);
    mouseY = $state(0);

    constructor() {
        if (typeof window !== "undefined") {
            window.addEventListener("mousemove", (e) => {
                this.mouseX = e.clientX;
                this.mouseY = e.clientY;
            });
        }
    }
}

export const botStore = new BotStore();
export const uiStore = new UIStore();

let ws: WebSocket | null = null;

export function clearEventLog() {
    botStore.events = [];
}

export function connectToBot() {
    if (ws) ws.close();

    botStore.connectionStatus = "connecting";
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const host = window.location.host;
    ws = new WebSocket(`${protocol}//${host}/ui/ws`);

    ws.onopen = () => {
        botStore.connectionStatus = "connected";
        console.log("Connected to Synaptic-MC Gateway");
    };

    ws.onclose = () => {
        botStore.connectionStatus = "disconnected";
        console.log("Disconnected from Gateway, retrying...");
        setTimeout(connectToBot, 2000);
    };

    ws.onmessage = (event) => {
        try {
            const message = JSON.parse(event.data);
            if (!message.type || !message.payload) return;

            const type = message.type.toUpperCase();

            // Helper to unwrap Go's VersionedState { Version: 1, State: { ... } }
            const unwrapState = (payload: any) => {
                if (payload && payload.State) return payload.State;
                if (payload && payload.state) return payload.state;
                return payload;
            };

            if (type === "STATE_UPDATE" || type === "STATE_SYNC") {
                const state = unwrapState(message.payload);
                botStore.gameState = state;
                controller.onStateUpdate(state);
                return;
            }

            if (type === "EVENT_STREAM") {
                const goEvent = message.payload;
                let innerPayload: any = {};

                const rawPayload = goEvent.payload || goEvent.Payload;
                if (rawPayload) {
                    try {
                        innerPayload =
                            typeof rawPayload === "string"
                                ? JSON.parse(rawPayload)
                                : rawPayload;
                    } catch {
                        innerPayload = rawPayload;
                    }
                }

                const eventType = (
                    goEvent.type ||
                    goEvent.Type ||
                    "UNKNOWN"
                ).toUpperCase();

                if (
                    eventType === "STATE_UPDATED" ||
                    eventType === "STATE_TICK"
                ) {
                    const state = unwrapState(innerPayload);
                    botStore.gameState = state;
                    controller.onStateUpdate(state);

                    // Overwrite innerPayload with unwrapped state for the event log
                    innerPayload = state;
                }

                if (eventType === "PLAN_CREATED" && innerPayload.objective) {
                    botStore.objective = innerPayload.objective;
                }

                botStore.addEvent({
                    type: eventType,
                    payload: innerPayload,
                    timestamp: new Date().toLocaleTimeString(),
                });
                if (eventType === "TASK_START" && innerPayload.action) {
                    const task: Task = {
                        id:
                            innerPayload.command_id ||
                            innerPayload.id ||
                            "task-" + Date.now(),
                        type: innerPayload.action,
                        completed: false,
                        target: innerPayload.target ?? null,
                    };
                    if (commitment.shouldCommit(task)) {
                        prefetcher.onTaskStart(task);
                    }
                }

                if (eventType === "TASK_END") {
                    commitment.reset();
                }
            }
        } catch (err) {
            console.error("WS Message Error:", err);
        }
    };
}

export function disconnectBot() {
    if (ws) {
        ws.close();
        ws = null;
    }
    botStore.connectionStatus = "disconnected";
}

export function sendCommand(type: string, payload: any) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type, payload }));
    }
}
