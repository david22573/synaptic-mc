// js/lib/models.ts

export enum ActionType {
    Gather = "gather",
    Craft = "craft",
    Hunt = "hunt",
    Explore = "explore",
    Build = "build",
    Smelt = "smelt",
    Farm = "farm",
    Mine = "mine",
    MarkLocation = "mark_location",
    RecallLocation = "recall_location",
    Idle = "idle",
    Sleep = "sleep",
    Retreat = "retreat",
    Eat = "eat",
    Interact = "interact",
    Store = "store",
    Retrieve = "retrieve",
    Look = "look",
    CameraMove = "camera_move",
}

export enum ClientEventType {
    TaskCompleted = "task_completed",
    TaskFailed = "task_failed",
    TaskAborted = "task_aborted",
    Death = "death",
    PanicRetreat = "panic_retreat",
}

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
    timeout?: number; // In nanoseconds, as Go's time.Duration is int64 nanoseconds
}

// Legacy name for Action, kept for compatibility where needed but mapped to Action
export type ActionIntent = Action;

export interface ActiveTask extends Action {
    startTime: number;
}

export interface Threat {
    name: string;
    distance: number;
}

// Legacy name for Threat, kept for compatibility
export interface ThreatInfo extends Threat {
    id?: number;
    threatScore?: number;
    position?: Vec3;
    entity?: any;
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

export interface ExecutionResult {
    action: Action;
    success: boolean;
    cause: string;
    progress: number;
}
