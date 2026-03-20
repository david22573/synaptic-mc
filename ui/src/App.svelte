<script lang="ts">
    import { onMount, onDestroy } from "svelte";

    type Vec3 = { x: number; y: number; z: number };
    type Threat = { name: string };
    type InventoryItem = { name: string; count: number };

    type BotState = {
        health: number;
        food: number;
        time_of_day: number;
        has_bed_nearby: boolean;
        position: Vec3;
        threats: Threat[];
        inventory: InventoryItem[];
    };

    let state: BotState = {
        health: 20,
        food: 20,
        time_of_day: 0,
        has_bed_nearby: false,
        position: { x: 0, y: 0, z: 0 },
        threats: [],
        inventory: [],
    };

    let ws: WebSocket;
    let connected = false;

    onMount(() => {
        connect();
    });

    onDestroy(() => {
        if (ws) ws.close();
    });

    function connect() {
        ws = new WebSocket("ws://localhost:8080/ui/ws");

        ws.onopen = () => {
            connected = true;
        };

        ws.onmessage = (event) => {
            try {
                const msg = JSON.parse(event.data);
                // Fixed: Go backend sends 'state_update', not 'state'
                if (msg.type === "state_update") {
                    state = msg.payload;
                }
            } catch (err) {
                console.error("Failed to parse WS message:", err);
            }
        };

        ws.onclose = () => {
            connected = false;
            setTimeout(connect, 2000);
        };
    }

    // Calculate day/night tint
    $: isNight = state.time_of_day >= 12000 && state.time_of_day <= 24000;
    $: darkness = isNight
        ? Math.sin(((state.time_of_day - 12000) / 12000) * Math.PI) * 0.6
        : 0;

    // Split inventory into hotbar (first 9) and side panel (the rest)
    $: hotbarItems = Array.from(
        { length: 9 },
        (_, i) => state.inventory[i] || null,
    );
    $: sideInventory = state.inventory.slice(9);

    // Format names for cleaner display (e.g., "oak_planks" -> "Oak Planks")
    function formatName(name: string) {
        return name
            .split("_")
            .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
            .join(" ");
    }
</script>

<svelte:head>
    <link
        href="https://fonts.googleapis.com/css2?family=VT323&display=swap"
        rel="stylesheet"
    />
</svelte:head>

<main class="dashboard">
    <iframe src="http://localhost:3000" title="Prismarine Viewer"></iframe>

    <div
        class="night-tint"
        style="background-color: rgba(5, 5, 20, {darkness});"
    ></div>

    <div class="hud">
        <div class="top-left">
            <div class="coord-box">
                XYZ: {Math.floor(state.position.x)}, {Math.floor(
                    state.position.y,
                )}, {Math.floor(state.position.z)}
            </div>
            <div class="status-box {connected ? 'online' : 'offline'}">
                {connected ? "LINK ESTABLISHED" : "SEARCHING FOR SIGNAL..."}
            </div>
        </div>

        <div class="side-inventory">
            {#each sideInventory as item}
                <div class="side-item">
                    <span class="item-count">{item.count}</span>
                    <span class="item-name">{formatName(item.name)}</span>
                </div>
            {/each}
        </div>

        <div class="bottom-center">
            {#if state.threats && state.threats.length > 0}
                <div class="threat-warning">
                    HOSTILES NEARBY: {state.threats
                        .map((t) => formatName(t.name))
                        .join(", ")}
                </div>
            {/if}

            <div class="stat-bars">
                <div class="bar-wrapper">
                    <div
                        class="bar-fill health-fill"
                        style="width: {(state.health / 20) * 100}%"
                    ></div>
                    <span class="bar-text">HP {state.health}/20</span>
                </div>
                <div class="bar-wrapper">
                    <div
                        class="bar-fill food-fill"
                        style="width: {(state.food / 20) * 100}%"
                    ></div>
                    <span class="bar-text">HUNGER {state.food}/20</span>
                </div>
            </div>

            <div class="hotbar">
                {#each hotbarItems as item, i}
                    <div class="hotbar-slot">
                        <span class="slot-number">{i + 1}</span>
                        {#if item}
                            <div class="slot-content">
                                <span class="slot-item-name"
                                    >{formatName(item.name).substring(
                                        0,
                                        4,
                                    )}</span
                                >
                                {#if item.count > 1}
                                    <span class="slot-count">{item.count}</span>
                                {/if}
                            </div>
                        {/if}
                    </div>
                {/each}
            </div>
        </div>
    </div>
</main>

<style>
    :global(body) {
        margin: 0;
        padding: 0;
        overflow: hidden;
        background: #000;
    }

    .dashboard {
        position: relative;
        width: 100vw;
        height: 100vh;
        font-family: "VT323", monospace;
        font-size: 20px;
        color: white;
        text-shadow: 2px 2px 0px #3f3f3f;
        user-select: none;
    }

    iframe {
        position: absolute;
        top: 0;
        left: 0;
        width: 100%;
        height: 100%;
        border: none;
        z-index: 1;
    }

    .night-tint {
        position: absolute;
        top: 0;
        left: 0;
        width: 100%;
        height: 100%;
        pointer-events: none;
        z-index: 2;
        transition: background-color 1s linear;
    }

    .hud {
        position: absolute;
        top: 0;
        left: 0;
        width: 100%;
        height: 100%;
        pointer-events: none;
        z-index: 3;
        display: flex;
        flex-direction: column;
        justify-content: space-between;
    }

    /* Top Left Elements */
    .top-left {
        padding: 10px;
        display: flex;
        flex-direction: column;
        gap: 5px;
    }

    .coord-box,
    .status-box {
        background: rgba(0, 0, 0, 0.4);
        display: inline-block;
        padding: 2px 8px;
        border: 2px solid rgba(0, 0, 0, 0.6);
        width: fit-content;
    }

    .status-box.online {
        color: #55ff55;
    }
    .status-box.offline {
        color: #ff5555;
    }

    /* Left Side Inventory (Like the screenshot) */
    .side-inventory {
        position: absolute;
        left: 10px;
        top: 30%;
        display: flex;
        flex-direction: column;
        gap: 4px;
        background: rgba(0, 0, 0, 0.2);
        padding: 10px;
        border-radius: 4px;
    }

    .side-item {
        display: flex;
        align-items: center;
        gap: 10px;
    }

    .item-count {
        color: #ffff55; /* Minecraft Yellow */
        min-width: 25px;
        text-align: right;
    }

    .item-name {
        color: #ffffff;
    }

    /* Bottom Center Area */
    .bottom-center {
        position: absolute;
        bottom: 10px;
        left: 50%;
        transform: translateX(-50%);
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 8px;
    }

    .threat-warning {
        color: #ff5555;
        background: rgba(0, 0, 0, 0.6);
        padding: 4px 12px;
        border: 2px solid #ff5555;
        animation: blink 1s infinite;
    }

    @keyframes blink {
        0%,
        100% {
            opacity: 1;
        }
        50% {
            opacity: 0.5;
        }
    }

    /* Health & Hunger Bars */
    .stat-bars {
        display: flex;
        gap: 20px;
        width: 100%;
        justify-content: center;
    }

    .bar-wrapper {
        position: relative;
        width: 160px;
        height: 20px;
        background: rgba(0, 0, 0, 0.5);
        border: 2px solid #222;
        border-top-color: #555;
        border-left-color: #555;
    }

    .bar-fill {
        height: 100%;
        transition: width 0.2s;
    }

    .health-fill {
        background: #ff5555;
        border-right: 2px solid #aa0000;
    }
    .food-fill {
        background: #ffaa00;
        border-right: 2px solid #cc6600;
    }

    .bar-text {
        position: absolute;
        top: -2px;
        left: 0;
        width: 100%;
        text-align: center;
        font-size: 18px;
        text-shadow: 1px 1px 0px #000;
    }

    /* Hotbar Styling */
    .hotbar {
        display: flex;
        background: rgba(0, 0, 0, 0.4);
        padding: 2px;
        border: 2px solid #222;
        border-top-color: #555;
        border-left-color: #555;
    }

    .hotbar-slot {
        position: relative;
        width: 40px;
        height: 40px;
        background: rgba(139, 139, 139, 0.4);
        border: 2px solid #373737;
        border-bottom-color: #fff;
        border-right-color: #fff;
        margin: 2px;
        display: flex;
        align-items: center;
        justify-content: center;
    }

    .slot-number {
        position: absolute;
        top: 2px;
        left: 4px;
        font-size: 14px;
        color: rgba(255, 255, 255, 0.5);
        text-shadow: none;
    }

    .slot-content {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        width: 100%;
        height: 100%;
    }

    .slot-item-name {
        font-size: 14px;
        color: white;
        text-transform: uppercase;
    }

    .slot-count {
        position: absolute;
        bottom: -2px;
        right: 2px;
        font-size: 18px;
        color: #fff;
        text-shadow: 2px 2px 0px #000;
    }
</style>
