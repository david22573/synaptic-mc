export interface Task {
    id: string;
    type: string;
    completed: boolean;
    next?: Task;
    target?: any;
    resources?: any[];
}

export class TaskCommitment {
    private commitmentTicks: number = 0;
    private readonly minCommitment: number = 10; // ticks to lock in

    shouldCommit(task: Task): boolean {
        if (task.completed) {
            this.commitmentTicks = 0;
            return false;
        }

        // Lock in movement tasks so the agent doesn't twitch if the planner
        // fast-path re-evaluates rapidly on slight position changes.
        if (task.type === "move" && this.commitmentTicks < this.minCommitment) {
            this.commitmentTicks++;
            return true;
        }

        return false;
    }

    reset() {
        this.commitmentTicks = 0;
    }
}
