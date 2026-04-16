// js/lib/utils/circuit_breaker.ts
export interface CircuitBreakerConfig {
    initialDelayMs: number;
    maxDelayMs: number;
    maxRetries: number;
    multiplier: number;
}

export class CircuitBreaker {
    private config: CircuitBreakerConfig;
    private failureCount: number = 0;
    private lastAttemptTime: number = 0;
    private state: 'CLOSED' | 'OPEN' | 'HALF_OPEN' = 'CLOSED';

    constructor(config?: Partial<CircuitBreakerConfig>) {
        this.config = {
            initialDelayMs: 500,
            maxDelayMs: 30000, // 30 seconds max backoff
            maxRetries: 10,
            multiplier: 2,
            ...config
        };
    }

    /**
     * Checks if the action is allowed to execute based on current backoff state.
     */
    public canExecute(): boolean {
        if (this.state === 'CLOSED') {
            return true;
        }

        const now = Date.now();
        const currentDelay = Math.min(
            this.config.initialDelayMs * Math.pow(this.config.multiplier, this.failureCount - 1),
            this.config.maxDelayMs
        );

        if (now - this.lastAttemptTime >= currentDelay) {
            this.state = 'HALF_OPEN';
            return true;
        }

        return false;
    }

    /**
     * Call this when the wrapped action (e.g., pathfinding) fails.
     */
    public recordFailure(): void {
        this.failureCount++;
        this.lastAttemptTime = Date.now();
        this.state = 'OPEN';
    }

    /**
     * Call this when the wrapped action succeeds to reset the breaker.
     */
    public recordSuccess(): void {
        this.failureCount = 0;
        this.lastAttemptTime = 0;
        this.state = 'CLOSED';
    }

    /**
     * Helper to execute an async function with the breaker logic applied.
     */
    public async execute<T>(action: () => Promise<T>, fallback?: () => T | Promise<T>): Promise<T | null> {
        if (!this.canExecute()) {
            if (fallback) {
                return fallback();
            }
            return null;
        }

        try {
            const result = await action();
            this.recordSuccess();
            return result;
        } catch (error) {
            this.recordFailure();
            throw error;
        }
    }
    
    public getFailureCount(): number {
        return this.failureCount;
    }
}
