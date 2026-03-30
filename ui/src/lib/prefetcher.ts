import type { Task } from "./task-commitment";

export class Prefetcher {
    private loadedChunks: Set<string> = new Set();
    private loadedResources: Set<string> = new Set();

    onTaskStart(task: Task) {
        if (task.next) {
            if (task.next.target) {
                this.preloadChunks(task.next.target);
            }
            if (task.next.resources) {
                this.preloadResources(task.next.resources);
            }
        }
    }

    private preloadChunks(target: { x?: number; z?: number }) {
        if (target.x === undefined || target.z === undefined) return;

        // Naive chunk coordinate calculation
        const chunkX = Math.floor(target.x / 16);
        const chunkZ = Math.floor(target.z / 16);
        const chunkId = `${chunkX},${chunkZ}`;

        if (!this.loadedChunks.has(chunkId)) {
            console.debug(`[Prefetcher] Preloading chunk: ${chunkId}`);
            // Fire off background request to the Go backend or local cache
            // to ensure block data is ready before the bot gets there.
            this.loadedChunks.add(chunkId);
        }
    }

    private preloadResources(resources: { id?: string; name?: string }[]) {
        for (const res of resources) {
            const resId = res.name || res.id;
            if (resId && !this.loadedResources.has(resId)) {
                console.debug(`[Prefetcher] Preloading resource: ${resId}`);
                // Fire off asset/recipe fetches
                this.loadedResources.add(resId);
            }
        }
    }

    clearCache() {
        this.loadedChunks.clear();
        this.loadedResources.clear();
    }
}
