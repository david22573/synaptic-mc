export interface DecisionTarget {
    type: string;
    name: string;
}

export interface IncomingDecision {
    id: string;
    action: string;
    target: DecisionTarget;
    rationale?: string;
}

export interface ActiveTask {
    id: string;
    action: string;
    target: DecisionTarget;
    startTime: number;
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
