/**
 * Base class for all bot-specific execution errors.
 */
export class ExecutionError extends Error {
    constructor(
        message: string,
        public cause: string,
        public progress: number,
    ) {
        super(message);
        this.name = "ExecutionError";
    }
}

/**
 * Thrown when a task is explicitly aborted via AbortSignal.
 */
export class TaskAbortError extends ExecutionError {
    constructor(message: string = "aborted", progress: number = 0) {
        super(message, "aborted", progress);
        this.name = "TaskAbortError";
    }
}

/**
 * Thrown when the bot cannot find a required block or entity in its vicinity.
 */
export class NoTargetsNearbyError extends ExecutionError {
    constructor(targetName: string, message?: string) {
        const msg = message || `NO_${targetName.toUpperCase()}_NEARBY`;
        super(msg, "MISSING_RESOURCE", 0);
        this.name = "NoTargetsNearbyError";
    }
}

/**
 * Thrown when the bot is physically unable to move (stuck).
 */
export class StuckError extends ExecutionError {
    constructor(message: string = "bot physically stuck during movement") {
        super(message, "STUCK", 0);
        this.name = "StuckError";
    }
}

export class StuckTerrainError extends ExecutionError {
    constructor(message: string = "STUCK_TERRAIN") {
        super(message, "STUCK_TERRAIN", 0);
        this.name = "StuckTerrainError";
    }
}

export class BlockedMobError extends ExecutionError {
    constructor(message: string = "BLOCKED_MOB") {
        super(message, "BLOCKED_MOB", 0);
        this.name = "BlockedMobError";
    }
}

export class NoToolError extends ExecutionError {
    constructor(message: string = "NO_TOOL") {
        super(message, "NO_TOOL", 0);
        this.name = "NoToolError";
    }
}

export class UnreachableError extends ExecutionError {
    constructor(message: string = "UNREACHABLE") {
        super(message, "UNREACHABLE", 0);
        this.name = "UnreachableError";
    }
}

/**
 * Thrown when a resource (item, tool, etc.) is missing.
 */
export class MissingResourceError extends ExecutionError {
    constructor(resourceName: string, message?: string) {
        const msg = message || `MISSING_RESOURCE: ${resourceName}`;
        super(msg, "MISSING_RESOURCE", 0);
        this.name = "MissingResourceError";
    }
}

/**
 * Helper to check if an error is an abort error.
 */
export function isAbortError(err: any): boolean {
    return (
        err instanceof TaskAbortError ||
        err?.message === "aborted" ||
        err?.name === "AbortError"
    );
}
