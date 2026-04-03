// ui/src/lib/task-commitment.ts
export interface Task {
    id: string;
    type: string;
    completed: boolean;
    priority?: number;
    next?: Task;
    target?: any;
    resources?: any[];
}

export class TaskCommitment {
    private commitmentTicks: number = 0;
    private minCommitment: number = 10; // ticks to lock in
    private lockedTaskType: string | null = null;

    shouldCommit(task: Task, isPanicMode: boolean = false): boolean {
        if (task.completed) {
            this.reset();
            return false;
        }

        // Week 5: Panic Mode bypasses all hard UI commitments
        if (isPanicMode) {
            this.reset();
            return false;
        }

        // Week 5: Hard Commitment Enforcement
        if (this.lockedTaskType && this.lockedTaskType !== task.type) {
            if (this.commitmentTicks < this.minCommitment) {
                this.commitmentTicks++;
                return true; // Enforce the lock, reject overriding plan
            }
        }

        if (
            task.type === "move" ||
            task.type === "craft" ||
            task.type === "mine"
        ) {
            if (this.commitmentTicks < this.minCommitment) {
                this.lockedTaskType = task.type;
                this.commitmentTicks++;
                return true;
            }
        }

        return false;
    }

    reset() {
        this.commitmentTicks = 0;
        this.lockedTaskType = null;
    }

    setCommitmentDuration(ticks: number) {
        this.minCommitment = Math.max(0, ticks);
    }
}
