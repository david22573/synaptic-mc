export type Vec3 = { x: number; y: number; z: number };

export interface AgentState {
    path?: Vec3[];
    position: Vec3;
    health: number;
    food: number;
}

export class AgentController {
    private targetPosition: Vec3 | null = null;
    private smoothSpeed: number = 0.15;

    // This represents the visual entity's position in the scene,
    // detached from the strict backend state position.
    public entityPosition: Vec3 = { x: 0, y: 0, z: 0 };

    constructor(initialPosition?: Vec3) {
        if (initialPosition) {
            this.entityPosition = { ...initialPosition };
        }
    }

    onStateUpdate(state: AgentState) {
        // Grab the immediate next node in the path if it exists
        if (state.path && state.path.length > 0) {
            this.targetPosition = state.path[0];
        } else {
            // Fallback to the raw state position if not pathing
            this.targetPosition = state.position;
        }
    }

    update() {
        if (!this.targetPosition) return;

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
