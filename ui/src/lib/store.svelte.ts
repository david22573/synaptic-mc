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

export interface FailureLog {
    plan_id: string;
    reason: string;
    state_snapshot: string;
    failure_count: number;
}

// Global instances for logic management
export const controller = new AgentController();
const prefetcher = new Prefetcher();
const commitment = new TaskCommitment();

class BotStore {
    gameState = $state<GameState | null>(null);
    objective = $state<string>("Initializing...");
    events = $state<BotEvent[]>([]);
    activeFailure = $state<FailureLog | null>(null);
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
let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

export function clearEventLog() {
    botStore.events = [];
    botStore.activeFailure = null;
}

export function connectToBot() {
    if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
    }

    if (ws) {
        ws.onclose = null;
        ws.close();
    }

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
        reconnectTimer = setTimeout(connectToBot, 2000);
    };

    ws.onmessage = (event) => {
        try {
            const message = JSON.parse(event.data);
            if (!message?.type || !("payload" in message)) return;

            const type = message.type.toUpperCase();

            const unwrapState = (payload: any) => {
                if (payload && payload.State) return payload.State;
                if (payload && payload.state) return payload.state;
                return payload;
            };

            const unwrapBroadcastPayload = (payload: any) => {
                if (payload && typeof payload === "object" && "payload" in payload) {
                    return payload.payload;
                }
                return payload;
            };

            if (type === "STATE_UPDATE" || type === "STATE_SYNC") {
                const state = unwrapState(unwrapBroadcastPayload(message.payload));
                botStore.gameState = state;
                controller.onStateUpdate(state);
                return;
            }

            // Capture the failure log
            if (type === "FAILURE_LOG") {
                const failure = message.payload as FailureLog;
                botStore.activeFailure = failure;
                botStore.addEvent({
                    type: "PLAN_FAILED",
                    payload: { reason: failure.reason, count: failure.failure_count },
                    timestamp: new Date().toLocaleTimeString(),
                });
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
                    innerPayload = state;
                }

                if (eventType === "PLAN_CREATED" && innerPayload.objective) {
                    botStore.objective = innerPayload.objective;
                    // Clear the failure warning when a new plan is created
                    botStore.activeFailure = null;
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
    if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
    }
    if (ws) {
        ws.onclose = null;
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
