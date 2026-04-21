<script lang="ts">
    import { onMount } from 'svelte';

    let stats = $state({
        deaths: 0,
        tasks_completed: 0,
        resources_gathered: 0,
        path_failures: 0,
        skill_reuse: 0,
        survival_time: 0
    });

    let startTime = Date.now();

    async function fetchStats() {
        try {
            const res = await fetch('/api/stats');
            if (res.ok) {
                stats = await res.json();
            }
        } catch (err) {
            console.error('Failed to fetch stats', err);
        }
    }

    onMount(() => {
        const interval = setInterval(fetchStats, 2000);
        return () => clearInterval(interval);
    });

    function formatTime(seconds: number) {
        const h = Math.floor(seconds / 3600);
        const m = Math.floor((seconds % 3600) / 60);
        const s = seconds % 60;
        return `${h}h ${m}m ${s}s`;
    }

    let hoursElapsed = $derived(Math.max((Date.now() - startTime) / 3600000, 0.01));
    let deathsPerHour = $derived((stats.deaths / hoursElapsed).toFixed(2));
    let tasksPerHour = $derived((stats.tasks_completed / hoursElapsed).toFixed(2));
    let resourcesPerHour = $derived((stats.resources_gathered / hoursElapsed).toFixed(2));
    let totalAttempts = $derived(stats.tasks_completed + stats.path_failures);
    let stuckRate = $derived(totalAttempts > 0 ? ((stats.path_failures / totalAttempts) * 100).toFixed(1) : "0");
</script>

<div class="metrics-panel">
    <h3>Scientific Performance</h3>
    
    <div class="grid">
        <div class="stat">
            <span class="label">Survival Time</span>
            <div class="value">{formatTime(stats.survival_time)}</div>
        </div>
        <div class="stat">
            <span class="label">Deaths / Hour</span>
            <div class="value" class:bad={Number(deathsPerHour) > 2}>{deathsPerHour}</div>
        </div>
        <div class="stat">
            <span class="label">Tasks / Hour</span>
            <div class="value">{tasksPerHour}</div>
        </div>
        <div class="stat">
            <span class="label">Resources / Hour</span>
            <div class="value">{resourcesPerHour}</div>
        </div>
        <div class="stat">
            <span class="label">Stuck Rate</span>
            <div class="value">{stuckRate}%</div>
        </div>
        <div class="stat">
            <span class="label">Skill Reuse</span>
            <div class="value">{stats.skill_reuse}</div>
        </div>
    </div>
</div>

<style>
    .metrics-panel {
        background: rgba(0, 0, 0, 0.5);
        border: 1px solid #444;
        padding: 1rem;
        border-radius: 4px;
        color: #eee;
    }
    h3 { margin-top: 0; color: #aaa; font-size: 0.9rem; text-transform: uppercase; }
    .grid {
        display: grid;
        grid-template-columns: repeat(2, 1fr);
        gap: 1rem;
    }
    .stat { display: flex; flex-direction: column; }
    .label { font-size: 0.7rem; color: #888; }
    .value { font-family: monospace; font-size: 1.2rem; }
    .bad { color: #ff5555; }
</style>
