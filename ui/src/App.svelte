<script lang="ts">
    import { onMount } from "svelte";
    import ItemIcon from "./components/ItemIcon.svelte";

    let gameState = $state<any>(null);
    let events = $state<any[]>([]);
    let objective = $state<string>("Initializing...");
    let connectionStatus = $state<"connecting" | "connected" | "disconnected">(
        "connecting",
    );

    let ws: WebSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let reconnectAttempts = 0;
    const maxEvents = 50;

    const position = $derived(gameState?.position || { x: 0, y: 0, z: 0 });
    const health = $derived(gameState?.health ?? 20);
    const food = $derived(gameState?.food ?? 20);
    const timeOfDay = $derived(gameState?.time_of_day ?? 0);
    const inventory = $derived(gameState?.inventory || []);
    const threats = $derived(gameState?.threats || []);
    const pois = $derived(gameState?.pois || []);

    const timeDisplay = $derived(formatTimeOfDay(timeOfDay));
    const coordsDisplay = $derived(
        `X: ${Math.round(position.x)} Y: ${Math.round(position.y)} Z: ${Math.round(position.z)}`,
    );

    let viewerUrl = $state("about:blank");

    const fullHearts = $derived(Math.max(0, Math.floor(health / 2)));
    const halfHearts = $derived(health % 2 !== 0 && health > 0 ? 1 : 0);
    const emptyHearts = $derived(Math.max(0, 10 - fullHearts - halfHearts));

    const fullFood = $derived(Math.max(0, Math.floor(food / 2)));
    const halfFood = $derived(food % 2 !== 0 && food > 0 ? 1 : 0);
    const emptyFood = $derived(Math.max(0, 10 - fullFood - halfFood));

    const hotbarSlots = $derived(
        Array.from({ length: 9 }, (_, i) => inventory[i] || null),
    );

    function formatTimeOfDay(time: number): string {
        const hours = Math.floor((time / 1000 + 6) % 24);
        const minutes = Math.floor(((time % 1000) / 1000) * 60);
        return `${hours.toString().padStart(2, "0")}:${minutes.toString().padStart(2, "0")}`;
    }

    function connect() {
        connectionStatus = "connecting";
        const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
        const host = window.location.host;
        const wsUrl = `${protocol}//${host}/ui/ws`;

        ws = new WebSocket(wsUrl);

        ws.onopen = () => {
            connectionStatus = "connected";
            reconnectAttempts = 0;
            if (reconnectTimer) clearTimeout(reconnectTimer);
        };

        ws.onmessage = (event) => {
            try {
                const message = JSON.parse(event.data);
                handleMessage(message);
            } catch (err) {
                console.error("Failed to parse message:", err);
            }
        };

        ws.onclose = () => {
            connectionStatus = "disconnected";
            scheduleReconnect();
        };

        ws.onerror = (error) => {
            connectionStatus = "disconnected";
        };
    }

    function handleMessage(message: any) {
        if (!message.type || !message.payload) return;

        switch (message.type) {
            case "state_update":
                gameState = message.payload;
                break;
            case "event_stream": {
                const newEvent = {
                    ...message.payload,
                    timestamp: new Date().toLocaleTimeString(),
                };
                events = [newEvent, ...events].slice(0, maxEvents);
                break;
            }
            case "objective_update":
                objective = message.payload;
                break;
        }
    }

    function scheduleReconnect() {
        if (reconnectTimer) clearTimeout(reconnectTimer);
        const delay = Math.min(3000 * Math.pow(1.5, reconnectAttempts), 30000);
        reconnectAttempts++;
        reconnectTimer = setTimeout(() => {
            connect();
        }, delay);
    }

    function clearEventLog() {
        events = [];
    }

    onMount(() => {
        viewerUrl = `http://${window.location.hostname}:3000`;
        connect();
        return () => {
            if (ws) ws.close();
            if (reconnectTimer) clearTimeout(reconnectTimer);
        };
    });
</script>

<main class="container">
    <header class="header">
        <div class="title-group">
            <h1>Synaptic MC</h1>
            <span class="coords-badge">{timeDisplay} | {coordsDisplay}</span>
        </div>
        <div class="status">
            <span class="indicator {connectionStatus}"></span>
            <span class="status-text">
                {#if connectionStatus === "connecting"}
                    Connecting...
                {:else if connectionStatus === "connected"}
                    Connected
                {:else}
                    Disconnected
                {/if}
            </span>
        </div>
    </header>

    <div class="split-layout">
        <div class="main-column">
            <section class="card viewer-card">
                <iframe
                    src={viewerUrl}
                    title="Mineflayer Viewer"
                    class="viewer-iframe"
                ></iframe>

                <div class="mc-overlay">
                    <div class="mc-crosshair"></div>

                    <div class="mc-hud">
                        <div class="mc-bars">
                            <div class="mc-bar-half">
                                {#each Array(fullHearts) as _}
                                    <span class="mc-icon-stat">❤️</span>
                                {/each}
                                {#each Array(halfHearts) as _}
                                    <span class="mc-icon-stat">💔</span>
                                {/each}
                                {#each Array(emptyHearts) as _}
                                    <span class="mc-icon-stat empty">🖤</span>
                                {/each}
                            </div>

                            <div class="mc-bar-half right-align">
                                {#each Array(emptyFood) as _}
                                    <span class="mc-icon-stat empty">🍖</span>
                                {/each}
                                {#each Array(halfFood) as _}
                                    <span class="mc-icon-stat">🍗</span>
                                {/each}
                                {#each Array(fullFood) as _}
                                    <span class="mc-icon-stat">🍖</span>
                                {/each}
                            </div>
                        </div>

                        <div class="mc-hotbar-container">
                            <div class="mc-offhand slot"></div>

                            <div class="mc-hotbar">
                                {#each hotbarSlots as slot, i}
                                    <div class="slot {i === 0 ? 'active' : ''}">
                                        {#if slot}
                                            <ItemIcon
                                                name={slot.name}
                                                size={24}
                                            />
                                            {#if slot.count > 1}
                                                <span class="slot-count"
                                                    >{slot.count}</span
                                                >
                                            {/if}
                                        {/if}
                                    </div>
                                {/each}
                            </div>
                        </div>
                    </div>
                </div>
            </section>

            <section class="card objective-card">
                <h3>🎯 Current Objective</h3>
                <p class="objective">{objective}</p>
            </section>
        </div>

        <aside class="sidebar">
            <section class="card">
                <h3>🎒 Inventory ({inventory.length} items)</h3>
                {#if inventory.length > 0}
                    <div class="inventory-grid">
                        {#each inventory as item}
                            <div class="item">
                                <span class="item-name">{item.name}</span>
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
                            <li class="threat-item">
                                <span class="threat-name">{threat.name}</span>
                                <span class="threat-dist"
                                    >{threat.distance}m</span
                                >
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
                                <span class="poi-name">{poi.name}</span>
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
                    <button onclick={clearEventLog} class="clear-btn"
                        >Clear</button
                    >
                </div>
                <div class="events-list">
                    {#if events.length > 0}
                        {#each events as event}
                            <div class="event-item">
                                <span class="event-time">{event.timestamp}</span
                                >
                                <span class="event-type">{event.type}</span>
                                <span
                                    class="event-payload"
                                    title={JSON.stringify(event.payload)}
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
    </div>
</main>

<style>
    :global(body) {
        margin: 0;
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto,
            Oxygen, Ubuntu, Cantarell, sans-serif;
        background: #0f172a;
        color: #e2e8f0;
    }

    .container {
        max-width: 1600px;
        margin: 0 auto;
        padding: 1rem;
    }

    .header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 1.5rem;
        padding-bottom: 1rem;
        border-bottom: 1px solid #334155;
    }

    .title-group {
        display: flex;
        align-items: center;
        gap: 1rem;
    }

    .header h1 {
        margin: 0;
        color: #38bdf8;
    }

    .coords-badge {
        background: #1e293b;
        padding: 0.25rem 0.75rem;
        border-radius: 4px;
        font-family: monospace;
        font-size: 0.875rem;
        border: 1px solid #334155;
    }

    .split-layout {
        display: grid;
        grid-template-columns: 2fr 1fr;
        gap: 1.5rem;
    }

    @media (max-width: 1024px) {
        .split-layout {
            grid-template-columns: 1fr;
        }
    }

    .main-column,
    .sidebar {
        display: flex;
        flex-direction: column;
        gap: 1rem;
    }

    .status {
        display: flex;
        align-items: center;
        gap: 0.5rem;
    }
    .indicator {
        width: 12px;
        height: 12px;
        border-radius: 50%;
        display: inline-block;
    }
    .indicator.connecting {
        background: #fbbf24;
        animation: pulse 1s infinite;
    }
    .indicator.connected {
        background: #34d399;
    }
    .indicator.disconnected {
        background: #ef4444;
    }

    @keyframes pulse {
        0%,
        100% {
            opacity: 1;
        }
        50% {
            opacity: 0.5;
        }
    }

    .card {
        background: #1e293b;
        border: 1px solid #334155;
        border-radius: 8px;
        padding: 1rem;
    }

    .card h3 {
        margin: 0 0 0.5rem 0;
        color: #94a3b8;
        font-size: 0.875rem;
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    /* --- MINECRAFT HUD STYLES --- */
    .viewer-card {
        padding: 0;
        position: relative;
        overflow: hidden;
        height: 600px;
        border: 2px solid #000;
        border-radius: 4px;
    }

    .viewer-iframe {
        width: 100%;
        height: 100%;
        border: none;
        background: #000;
    }

    .mc-overlay {
        position: absolute;
        inset: 0;
        pointer-events: none;
        display: flex;
        flex-direction: column;
        justify-content: flex-end;
        align-items: center;
        padding-bottom: 12px;
    }

    .mc-crosshair {
        position: absolute;
        top: 50%;
        left: 50%;
        transform: translate(-50%, -50%);
        width: 16px;
        height: 16px;
        pointer-events: none;
    }

    .mc-crosshair::before,
    .mc-crosshair::after {
        content: "";
        position: absolute;
        background: rgba(255, 255, 255, 0.6);
        mix-blend-mode: difference;
    }

    .mc-crosshair::before {
        top: 7px;
        left: 0;
        width: 16px;
        height: 2px;
    }
    .mc-crosshair::after {
        top: 0;
        left: 7px;
        width: 2px;
        height: 16px;
    }

    .mc-hud {
        display: flex;
        flex-direction: column;
        align-items: center;
        width: 100%;
    }

    .mc-bars {
        display: flex;
        justify-content: space-between;
        width: 364px;
        margin-bottom: 4px;
    }

    .mc-bar-half {
        display: flex;
        gap: 1px;
    }

    .mc-icon-stat {
        font-size: 14px;
        line-height: 1;
        filter: drop-shadow(1px 1px 0 rgba(0, 0, 0, 0.8));
    }

    .mc-icon-stat.empty {
        filter: grayscale(1) brightness(0.2)
            drop-shadow(1px 1px 0 rgba(0, 0, 0, 0.8));
    }

    .mc-hotbar-container {
        display: flex;
        align-items: flex-end;
        gap: 8px;
    }

    .mc-hotbar {
        display: flex;
        background: #8b8b8b;
        border: 2px solid #111;
        padding: 2px;
        box-shadow:
            inset 2px 2px 0px rgba(255, 255, 255, 0.4),
            inset -2px -2px 0px rgba(0, 0, 0, 0.4);
    }

    .slot {
        width: 36px;
        height: 36px;
        background: #8b8b8b;
        border-style: solid;
        border-width: 2px;
        border-color: #373737 #fff #fff #373737;
        position: relative;
        display: flex;
        justify-content: center;
        align-items: center;
    }

    .mc-offhand {
        border-color: #373737 #fff #fff #373737;
    }

    .slot.active {
        outline: 3px solid rgba(255, 255, 255, 0.9);
        outline-offset: -3px;
        z-index: 10;
    }

    .slot-count {
        position: absolute;
        bottom: -2px;
        right: 1px;
        font-size: 14px;
        font-weight: 900;
        color: white;
        text-shadow:
            2px 2px 0 #3f3f3f,
            -1px -1px 0 #3f3f3f,
            1px -1px 0 #3f3f3f,
            -1px 1px 0 #3f3f3f;
    }

    /* --- SIDEBAR STYLES --- */
    .objective {
        font-size: 1.125rem;
        color: #38bdf8;
        margin: 0;
    }
    .inventory-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
        gap: 0.5rem;
        max-height: 250px;
        overflow-y: auto;
    }
    .item {
        display: flex;
        justify-content: space-between;
        padding: 0.5rem;
        background: #334155;
        border-radius: 4px;
        font-size: 0.875rem;
    }
    .item-name {
        font-family: monospace;
    }
    .item-count {
        font-weight: 600;
        color: #94a3b8;
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
        background: #334155;
        border-radius: 4px;
        font-size: 0.875rem;
    }
    .threat-name {
        color: #ef4444;
        font-weight: 600;
    }
    .poi-type {
        color: #94a3b8;
        font-size: 0.7rem;
        text-transform: uppercase;
    }
    .poi-name {
        font-family: monospace;
        margin-left: 0.5rem;
    }
    .events-card {
        display: flex;
        flex-direction: column;
        height: 100%;
        max-height: 400px;
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
        border-bottom: 1px solid #334155;
        display: flex;
        gap: 0.75rem;
    }
    .event-time {
        color: #94a3b8;
        min-width: 60px;
    }
    .event-type {
        color: #38bdf8;
        min-width: 100px;
    }
    .event-payload {
        color: #e2e8f0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .empty {
        color: #64748b;
        font-style: italic;
        margin: 0;
    }
</style>
