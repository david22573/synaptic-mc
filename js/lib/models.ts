export enum ActionType {
    Gather = "gather",
    Craft = "craft",
    Hunt = "hunt",
    Explore = "explore",
    Build = "build",
    Smelt = "smelt",
    MarkLocation = "mark_location",
    RecallLocation = "recall_location",
    Idle = "idle",
    Sleep = "sleep",
    Retreat = "retreat",
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

export interface DecisionTarget {
    type: string;
    name: string;
}

export interface IncomingDecision {
    id: string;
    action: ActionType;
    target: DecisionTarget;
    rationale?: string;
    trace?: TraceContext;
}

export interface ActiveTask {
    id: string;
    action: ActionType;
    target: DecisionTarget;
    startTime: number;
    trace: TraceContext;
}

export interface ThreatInfo {
    id: number;
    name: string;
    distance: number;
    threatScore: number;
    position: {
        x: number;
        y: number;
        z: number;
    };
    entity?: any;
}
