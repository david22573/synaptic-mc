// ui/src/lib/models.ts

export interface TraceContext {
    trace_id: string;
    action_id: string;
    milestone_id?: string;
}

export interface Vec3 {
    x: number;
    y: number;
    z: number;
}

export interface Target {
    type: string;
    name: string;
}

export interface Item {
    name: string;
    count: number;
}

export interface Action {
    id: string;
    controller_id?: string;
    source: string;
    trace: TraceContext;
    type?: string;
    action: string;
    target: Target;
    count: number;
    rationale: string;
    priority: number;
    timeout?: number;
}

export interface Threat {
    name: string;
    distance: number;
}

export interface POI {
    type: string;
    name: string;
    position: Vec3;
    distance: number;
    visibility: number;
    score: number;
    direction?: string;
}

export interface Feedback {
    type: string;
    cause: string;
    action?: string;
    hint?: string;
}

export interface ChunkCoord {
    x: number;
    z: number;
}

export interface DangerZone {
    center: Vec3;
    radius: number;
    reason: string;
    risk: number;
}

export interface Task {
    id: string;
    type: string;
    completed: boolean;
    priority?: number;
    next?: Task;
    target?: Target | null;
    resources?: any[];
}

export interface GameState {
    health: number;
    food: number;
    time_of_day: number;
    experience: number;
    level: number;
    has_bed_nearby: boolean;
    position: Vec3;
    threats: Threat[];
    pois: POI[];
    inventory: Item[];
    hotbar: (Item | null)[];
    offhand: Item | null;
    active_slot: number;
    known_chests?: Record<string, Item[]>;
    feedback?: Feedback[];
    current_task?: Action | null;
    task_progress?: number;
    danger_zones?: DangerZone[];
    visited_chunks?: ChunkCoord[];
    terrain_roughness?: Record<string, number>;
}
