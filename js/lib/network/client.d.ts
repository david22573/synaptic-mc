import * as models from "../models.js";
export interface ControlPlaneEvents {
    onCommand: (intent: models.ActionIntent) => void;
    onUnlock: () => void;
}
export declare class ControlPlaneClient {
    private ws;
    private readonly url;
    private readonly callbacks;
    private reconnectTimer;
    private reconnectAttempts;
    constructor(url: string, callbacks: ControlPlaneEvents);
    isConnected(): boolean;
    connect(): void;
    private scheduleReconnect;
    sendEvent(event: string, actionStr: string, commandId?: string, cause?: string, startTime?: number): void;
    sendState(state: Record<string, unknown>): void;
}
//# sourceMappingURL=client.d.ts.map