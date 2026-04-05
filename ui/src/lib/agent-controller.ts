import type { Vec3, GameState } from "./models";

function getDistance(a: Vec3, b: Vec3): number {
    const dx = a.x - b.x;
    const dy = a.y - b.y;
    const dz = a.z - b.z;
    return Math.sqrt(dx * dx + dy * dy + dz * dz);
}

// Week 6 Observability sync - UI specific extensions to GameState
export interface AgentState extends GameState {
    path?: Vec3[];
    isPanicMode?: boolean;
    isStuck?: boolean;
    timestamp?: number;
}

// Helper to handle both GameState and VersionedState (from Go)
function extractState(payload: any): AgentState {
    if (payload && payload.State) {
        // Use performance.now() to match AgentController's time base for smooth interpolation
        return { ...payload.State, timestamp: performance.now() };
    }
    return payload;
}

export class AgentController {
    private targetPosition: Vec3 | null = null;
    private smoothSpeed: number = 0.15;
    private lastServerUpdate: number = 0;
    private path: Vec3[] | null = null;

    public entityPosition: Vec3 = { x: 0, y: 0, z: 0 };
    public isStuck: boolean = false;
    public isPanicMode: boolean = false;

    constructor(initialPosition?: Vec3) {
        if (initialPosition) {
            this.entityPosition = { ...initialPosition };
        }
        this.lastServerUpdate = performance.now();
    }

    onStateUpdate(payload: any) {
        const state = extractState(payload);
        // Ensure we always have a valid performance-based timestamp
        this.lastServerUpdate = state.timestamp || performance.now();
        this.path = state.path || null;

        // Observability states
        this.isStuck = !!state.isStuck;
        this.isPanicMode = !!state.isPanicMode;

        if (state.path && state.path.length > 0) {
            this.targetPosition = state.path[0];
        } else {
            this.targetPosition = state.position;
        }
    }

    reconcileState(authoritativeState: AgentState) {
        const threshold = 2.0;
        const drift = getDistance(
            this.entityPosition,
            authoritativeState.position,
        );

        if (drift > threshold) {
            this.entityPosition = { ...authoritativeState.position };
            this.targetPosition = null;
        }

        if (this.path && this.path.length > 0) {
            const pathError = getDistance(this.entityPosition, this.path[0]);
            if (pathError > threshold * 2) {
                this.path = null;
            }
        }
    }

    update() {
        if (!this.targetPosition) return;

        const now = performance.now();
        const timeSinceUpdate = Math.max(now - this.lastServerUpdate, 1);

        // Snap faster if we are in panic mode
        const baseSpeed = this.isPanicMode ? 0.3 : 0.15;
        
        // Fix: If timeSinceUpdate is large, accelerate to catch up rather than slowing down.
        // We scale the speed linearly up to 1.0 (snap) if lag exceeds 500ms.
        const dynamicSpeed = Math.min(
            1.0,
            Math.max(baseSpeed, timeSinceUpdate / 500)
        );
        this.smoothSpeed = dynamicSpeed;

        this.entityPosition.x = this.lerp(
            this.entityPosition.x,
            this.targetPosition.x,
            this.smoothSpeed,
        );
        this.entityPosition.y = this.lerp(
            this.entityPosition.y,
            this.targetPosition.y,
            this.smoothSpeed,
        );
        this.entityPosition.z = this.lerp(
            this.entityPosition.z,
            this.targetPosition.z,
            this.smoothSpeed,
        );
    }

    private lerp(start: number, end: number, amt: number): number {
        return (1 - amt) * start + amt * end;
    }
}
