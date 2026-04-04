import type { Task, Target } from "./models";

export class Prefetcher {
    private loadedChunks: Set<string> = new Set();
    private loadedResources: Set<string> = new Set();

    onTaskStart(task: Task) {
        if (task.next) {
            if (task.next.target) {
                this.preloadChunks(task.next.target as any);
            }
            if (task.next.resources) {
                this.preloadResources(task.next.resources);
            }
        }
    }

    private preloadChunks(target: { x?: number; z?: number } & Target) {
        // If it's a structured target with coordinates (future-proofing)
        if (target.x !== undefined && target.z !== undefined) {
            this.doPreload(target.x, target.z);
            return;
        }

        // If it's a location target, try to parse coordinates from name if they exist
        if (target.type === "location" && target.name) {
            const match = target.name.match(/(-?\d+),\s*(-?\d+)/);
            if (match) {
                this.doPreload(parseInt(match[1]), parseInt(match[2]));
            }
        }
    }

    private doPreload(x: number, z: number) {
        // Naive chunk coordinate calculation
        const chunkX = Math.floor(x / 16);
        const chunkZ = Math.floor(z / 16);
        const chunkId = `${chunkX},${chunkZ}`;

        if (!this.loadedChunks.has(chunkId)) {
            console.debug(`[Prefetcher] Preloading chunk: ${chunkId}`);
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
