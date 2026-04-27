<script lang="ts">
    import { botStore, uiStore, clearEventLog } from "../lib/store.svelte";
    import ItemIcon from "./ItemIcon.svelte";
    import Metrics from "./Metrics.svelte";

    let { isOpen = true } = $props();

    const inventory = $derived(botStore.gameState?.inventory || []);
    const threats = $derived(botStore.gameState?.threats || []);
    const pois = $derived(botStore.gameState?.pois || []);

    function formatName(name: string) {
        if (!name) return "";
        return name.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
    }

    function setTooltip(e: MouseEvent, text: string) {
        if (!text) return;
        uiStore.mouseX = e.clientX;
        uiStore.mouseY = e.clientY;
        uiStore.tooltip = text;
    }

    function clearTooltip() {
        uiStore.tooltip = "";
    }
</script>

<aside class="sidebar" class:open={isOpen}>
    <section class="card objective-card">
        <h3>🎯 Current Objective</h3>
        <p class="objective">{botStore.objective}</p>
    </section>

    {#if botStore.gameState?.current_task}
        <section class="card task-card">
            <h3>🏃 Current Task</h3>
            <div class="task-info">
                <span class="task-action">{formatName(botStore.gameState.current_task.action)}</span>
                {#if botStore.gameState.current_task.target}
                    <span class="task-target">→ {formatName(botStore.gameState.current_task.target.name)}</span>
                {/if}
            </div>
            <div class="progress-container">
                <div class="progress-bar">
                    <div class="progress-fill" style="width: {(botStore.gameState.task_progress || 0) * 100}%"></div>
                </div>
                <span class="progress-text">{Math.round((botStore.gameState.task_progress || 0) * 100)}%</span>
            </div>
        </section>
    {/if}

    <section class="card stats-card">
        <h3>🌍 World & Stats</h3>
        <div class="stats-grid">
            <div class="stat-item">
                <span class="stat-label">Bed Nearby:</span>
                <span class="stat-value {botStore.gameState?.has_bed_nearby ? 'text-green' : 'text-red'}">
                    {botStore.gameState?.has_bed_nearby ? 'YES' : 'NO'}
                </span>
            </div>
            <div class="stat-item">
                <span class="stat-label">Health:</span>
                <span class="stat-value">{Math.round(botStore.gameState?.health || 0)}/20</span>
            </div>
            <div class="stat-item">
                <span class="stat-label">Food:</span>
                <span class="stat-value">{Math.round(botStore.gameState?.food || 0)}/20</span>
            </div>
        </div>
    </section>

    <Metrics />

    <section class="card">
        <h3>🎒 Inventory ({inventory.length} items)</h3>
        {#if inventory.length > 0}
            <div class="inventory-grid">
                {#each inventory as item}
                    <div
                        class="item"
                        role="presentation"
                        onmousemove={(e) =>
                            setTooltip(e, formatName(item.name))}
                        onmouseleave={clearTooltip}
                    >
                        <ItemIcon name={item.name} size={24} />
                        <span class="item-name">{formatName(item.name)}</span>
                        <span class="item-count">{item.count}</span>
                    </div>
                {/each}
            </div>
        {:else}
            <p class="empty">Empty</p>
        {/if}
    </section>

    <section class="card">
        <h3>⚠️ Threats ({threats.length})</h3>
        {#if threats.length > 0}
            <ul class="list">
                {#each threats as threat}
                    <li
                        class="threat-item"
                        role="presentation"
                        onmousemove={(e) =>
                            setTooltip(e, formatName(threat.name))}
                        onmouseleave={clearTooltip}
                    >
                        <span class="threat-name"
                            >{formatName(threat.name)}</span
                        >
                        <span class="threat-dist">{threat.distance}m</span>
                    </li>
                {/each}
            </ul>
        {:else}
            <p class="empty">No threats detected</p>
        {/if}
    </section>

    <section class="card">
        <h3>📡 POIs ({pois.length})</h3>
        {#if pois.length > 0}
            <ul class="list">
                {#each pois.slice(0, 10) as poi}
                    <li class="poi-item">
                        <span class="poi-type">{poi.type}</span>
                        <span
                            class="poi-name"
                            role="presentation"
                            onmousemove={(e) =>
                                setTooltip(e, formatName(poi.name))}
                            onmouseleave={clearTooltip}
                        >
                            {formatName(poi.name)}
                        </span>
                        <span class="poi-dist"
                            >{poi.distance}m {poi.direction}</span
                        >
                    </li>
                {/each}
            </ul>
        {:else}
            <p class="empty">No POIs detected</p>
        {/if}
    </section>

    <section class="card events-card">
        <div class="events-header">
            <h3>📋 Event Stream</h3>
            <button onclick={clearEventLog} class="clear-btn">Clear</button>
        </div>
        <div class="events-list">
            {#if botStore.events.length > 0}
                {#each botStore.events as event}
                    <div class="event-item">
                        <span class="event-time">{event.timestamp}</span>
                        <span class="event-type">{event.type}</span>
                        <span
                            class="event-payload"
                            role="presentation"
                            onmousemove={(e) =>
                                setTooltip(e, JSON.stringify(event.payload))}
                            onmouseleave={clearTooltip}
                        >
                            {#if event.type === "TASK_END" || event.type === "BOT_DEATH" || event.type === "PANIC_TRIGGERED"}
                                {event.payload.status || ""}
                                {event.payload.action || ""}
                                {event.payload.cause
                                    ? `(${event.payload.cause})`
                                    : ""}
                            {:else}
                                {JSON.stringify(event.payload)}
                            {/if}
                        </span>
                    </div>
                {/each}
            {:else}
                <p class="empty">No events yet</p>
            {/if}
        </div>
    </section>
</aside>

<style>
    .sidebar {
        position: absolute;
        top: 0;
        right: -420px;
        width: 420px;
        height: 100vh;
        background: rgba(15, 23, 42, 0.75);
        backdrop-filter: blur(12px);
        -webkit-backdrop-filter: blur(12px);
        border-left: 1px solid rgba(255, 255, 255, 0.1);
        z-index: 20;
        display: flex;
        flex-direction: column;
        gap: 1rem;
        padding: 1.5rem;
        box-sizing: border-box;
        overflow-y: auto;
        color: #e2e8f0;
        transition: right 0.3s cubic-bezier(0.4, 0, 0.2, 1);
        pointer-events: auto;
    }

    .sidebar.open {
        right: 0;
    }

    @media (max-width: 768px) {
        .sidebar {
            width: 320px;
            right: -320px;
            padding: 1rem;
            gap: 0.75rem;
        }
        .objective {
            font-size: 1rem;
        }
        .inventory-grid {
            grid-template-columns: repeat(auto-fill, minmax(90px, 1fr));
        }
    }

    @media (max-width: 480px) {
        .sidebar {
            width: 280px;
            right: -280px;
        }
    }

    .sidebar::-webkit-scrollbar {
        width: 6px;
    }

    .sidebar::-webkit-scrollbar-track {
        background: transparent;
    }

    .sidebar::-webkit-scrollbar-thumb {
        background: rgba(255, 255, 255, 0.2);
        border-radius: 3px;
    }

    .card {
        background: rgba(30, 41, 59, 0.7);
        border: 1px solid rgba(255, 255, 255, 0.1);
        border-radius: 8px;
        padding: 1rem;
        box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.1);
    }

    .card h3 {
        margin: 0 0 0.5rem 0;
        color: #94a3b8;
        font-size: 0.875rem;
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .objective {
        font-size: 1.125rem;
        color: #38bdf8;
        margin: 0;
        line-height: 1.4;
    }

    .task-info {
        display: flex;
        gap: 0.5rem;
        font-family: monospace;
        margin-bottom: 0.75rem;
        font-size: 1rem;
    }

    .task-action {
        color: #fbbf24;
        font-weight: bold;
    }

    .task-target {
        color: #94a3b8;
    }

    .progress-container {
        display: flex;
        align-items: center;
        gap: 0.75rem;
    }

    .progress-bar {
        flex-grow: 1;
        height: 8px;
        background: #1e293b;
        border-radius: 4px;
        overflow: hidden;
        border: 1px solid rgba(255, 255, 255, 0.1);
    }

    .progress-fill {
        height: 100%;
        background: #38bdf8;
        transition: width 0.3s ease;
    }

    .progress-text {
        font-family: monospace;
        font-size: 0.75rem;
        color: #94a3b8;
        min-width: 35px;
    }

    .stats-grid {
        display: grid;
        grid-template-columns: repeat(2, 1fr);
        gap: 0.75rem;
    }

    .stat-item {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .stat-label {
        font-size: 0.7rem;
        color: #64748b;
        text-transform: uppercase;
    }

    .stat-value {
        font-family: monospace;
        font-size: 0.9rem;
        font-weight: bold;
    }

    .text-green { color: #34d399; }
    .text-red { color: #ef4444; }

    .inventory-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
        gap: 0.5rem;
        max-height: 200px;
        overflow-y: auto;
    }

    .item {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        padding: 0.5rem;
        background: rgba(51, 65, 85, 0.8);
        border-radius: 4px;
        font-size: 0.875rem;
        cursor: help;
    }

    .item-name {
        font-family: monospace;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .item-count {
        font-family: monospace;
        font-size: 0.9rem;
        font-weight: bold;
        color: white;
        text-shadow: 1px 1px 0 #3f3f3f;
        margin-left: auto;
    }

    .list {
        list-style: none;
        padding: 0;
        margin: 0;
    }

    .threat-item,
    .poi-item {
        display: flex;
        justify-content: space-between;
        padding: 0.5rem;
        margin-bottom: 0.25rem;
        background: rgba(51, 65, 85, 0.8);
        border-radius: 4px;
        font-size: 0.875rem;
        cursor: help;
    }

    .threat-name {
        color: #ef4444;
        font-weight: 600;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        max-width: 90px;
    }

    .poi-type {
        color: #94a3b8;
        font-size: 0.7rem;
        text-transform: uppercase;
    }

    .poi-name {
        font-family: monospace;
        margin-left: 0.5rem;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        max-width: 90px;
        cursor: help;
    }

    .events-card {
        display: flex;
        flex-direction: column;
        flex-grow: 1;
        min-height: 250px;
    }

    .events-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.5rem;
    }

    .clear-btn {
        background: #475569;
        border: none;
        color: white;
        padding: 0.25rem 0.75rem;
        border-radius: 4px;
        cursor: pointer;
        font-size: 0.875rem;
        transition: background 0.2s;
    }

    .clear-btn:hover {
        background: #64748b;
    }

    .events-list {
        flex-grow: 1;
        overflow-y: auto;
        font-family: monospace;
        font-size: 0.75rem;
    }

    .event-item {
        padding: 0.5rem;
        border-bottom: 1px solid rgba(255, 255, 255, 0.05);
        display: flex;
        gap: 0.75rem;
    }

    .event-time {
        color: #94a3b8;
        min-width: 60px;
    }

    .event-type {
        color: #38bdf8;
        min-width: 90px;
    }

    .event-payload {
        color: #e2e8f0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        cursor: help;
    }

    .empty {
        color: #64748b;
        font-style: italic;
        margin: 0;
    }
</style>
