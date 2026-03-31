<script lang="ts">
    import { onMount } from "svelte";
    import {
        botStore,
        uiStore,
        connectToBot,
        disconnectBot,
        sendCameraControl,
    } from "./lib/store.svelte";
    import { controller } from "./lib/store.svelte";
    import HUD from "./components/HUD.svelte";
    import Sidebar from "./components/Sidebar.svelte";

    let viewerUrl = $state("about:blank");
    let isSidebarOpen = $state(true);
    let viewerIframe: HTMLIFrameElement;

    // 60 FPS Interpolation State
    let renderPosition = $state({ x: 0, y: 0, z: 0 });
    let animationFrameId: number;

    const position = $derived(
        botStore.gameState?.position || { x: 0, y: 0, z: 0 },
    );
    const timeOfDay = $derived(botStore.gameState?.time_of_day ?? 0);

    const timeDisplay = $derived(formatTimeOfDay(timeOfDay));

    const rawCoordsDisplay = $derived(
        `Raw X: ${Math.round(position.x)} Y: ${Math.round(position.y)} Z: ${Math.round(position.z)}`,
    );
    const smoothCoordsDisplay = $derived(
        `X: ${renderPosition.x.toFixed(2)} Y: ${renderPosition.y.toFixed(2)} Z: ${renderPosition.z.toFixed(2)}`,
    );

    function formatTimeOfDay(time: number): string {
        const hours = Math.floor((time / 1000 + 6) % 24);
        const minutes = Math.floor(((time % 1000) / 1000) * 60);
        return `${hours.toString().padStart(2, "0")}:${minutes.toString().padStart(2, "0")}`;
    }

    function renderLoop() {
        controller.update();
        renderPosition = { ...controller.entityPosition };
        animationFrameId = requestAnimationFrame(renderLoop);
    }

    onMount(() => {
        viewerUrl = `http://${window.location.hostname}:3000`;
        connectToBot();
        renderLoop();

        let lastMouseMove = 0;
        const onMouseMove = (e: MouseEvent) => {
            const now = performance.now();
            if (now - lastMouseMove < 50) return; // Throttle to roughly 20 FPS

            lastMouseMove = now;

            if (viewerIframe) {
                const rect = viewerIframe.getBoundingClientRect();
                const relativeX = e.clientX - rect.left;
                const relativeY = e.clientY - rect.top;

                // Only send controls if mouse is actively over the viewer area
                if (
                    relativeX >= 0 &&
                    relativeX <= rect.width &&
                    relativeY >= 0 &&
                    relativeY <= rect.height
                ) {
                    const yaw = (relativeX / rect.width - 0.5) * 180;
                    const pitch = (relativeY / rect.height - 0.5) * 90;
                    sendCameraControl(yaw, pitch);
                }
            }
        };

        window.addEventListener("mousemove", onMouseMove);

        return () => {
            disconnectBot();
            window.removeEventListener("mousemove", onMouseMove);
            if (animationFrameId) cancelAnimationFrame(animationFrameId);
        };
    });
</script>

<main class="fullscreen">
    <iframe
        bind:this={viewerIframe}
        src={viewerUrl}
        title="Mineflayer Viewer"
        class="viewer-iframe"
    ></iframe>

    <div class="floating-header">
        <div class="title-group">
            <h1>Synaptic MC</h1>
            <div class="coords-container">
                <span class="coords-badge"
                    >{timeDisplay} | {smoothCoordsDisplay} (60 FPS)</span
                >
                <span class="raw-coords">{rawCoordsDisplay} (50ms tick)</span>
            </div>
        </div>
        <div class="status">
            <span class="indicator {botStore.connectionStatus}"></span>
            <span class="status-text">
                {#if botStore.connectionStatus === "connecting"}
                    Connecting...
                {:else if botStore.connectionStatus === "connected"}
                    Connected
                {:else}
                    Disconnected
                {/if}
            </span>
        </div>
    </div>

    <HUD />
    <Sidebar isOpen={isSidebarOpen} />

    <button
        class="sidebar-toggle"
        class:sidebar-closed={!isSidebarOpen}
        onclick={() => (isSidebarOpen = !isSidebarOpen)}
        title="Toggle Sidebar"
    >
        {isSidebarOpen ? "◀" : "▶"}
    </button>

    {#if uiStore.tooltip}
        <div
            class="mc-tooltip"
            style="left: {uiStore.mouseX + 15}px; top: {uiStore.mouseY + 15}px;"
        >
            {uiStore.tooltip}
        </div>
    {/if}
</main>

<style>
    :global(body) {
        margin: 0;
        padding: 0;
        overflow: hidden;
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto,
            sans-serif;
        background: #000;
        color: #e2e8f0;
    }

    .fullscreen {
        position: relative;
        width: 100vw;
        height: 100vh;
        overflow: hidden;
    }

    .viewer-iframe {
        position: absolute;
        inset: 0;
        width: 100%;
        height: 100%;
        border: none;
        z-index: 1;
        pointer-events: auto;
    }

    .floating-header {
        position: absolute;
        top: 1rem;
        left: 1rem;
        z-index: 20;
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
        background: rgba(15, 23, 42, 0.7);
        backdrop-filter: blur(8px);
        -webkit-backdrop-filter: blur(8px);
        padding: 0.75rem 1.25rem;
        border-radius: 8px;
        border: 1px solid rgba(255, 255, 255, 0.1);
        pointer-events: auto;
    }

    .title-group {
        display: flex;
        align-items: flex-start;
        gap: 1rem;
    }

    .floating-header h1 {
        margin: 0;
        font-size: 1.25rem;
        color: #38bdf8;
        line-height: 1.2;
    }

    .coords-container {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .coords-badge {
        background: rgba(30, 41, 59, 0.8);
        padding: 0.25rem 0.5rem;
        border-radius: 4px;
        font-family: monospace;
        font-size: 0.875rem;
        border: 1px solid rgba(255, 255, 255, 0.1);
        color: #60a5fa;
    }

    .raw-coords {
        font-family: monospace;
        font-size: 0.7rem;
        color: #94a3b8;
        padding-left: 0.25rem;
    }

    .status {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        font-size: 0.875rem;
    }

    .indicator {
        width: 10px;
        height: 10px;
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

    .sidebar-toggle {
        position: absolute;
        top: 1rem;
        right: 430px;
        z-index: 25;
        background: rgba(15, 23, 42, 0.8);
        color: white;
        border: 1px solid rgba(255, 255, 255, 0.2);
        border-radius: 8px;
        width: 40px;
        height: 40px;
        display: flex;
        align-items: center;
        justify-content: center;
        cursor: pointer;
        backdrop-filter: blur(4px);
        transition: right 0.3s cubic-bezier(0.4, 0, 0.2, 1);
    }

    .sidebar-toggle:hover {
        background: rgba(30, 41, 59, 0.9);
    }

    .sidebar-toggle.sidebar-closed {
        right: 10px;
    }

    .mc-tooltip {
        position: fixed;
        background: rgba(16, 0, 16, 0.95);
        border: 2px solid #3700b3;
        border-radius: 3px;
        color: #fff;
        padding: 4px 8px;
        font-family: monospace;
        font-size: 14px;
        white-space: nowrap;
        z-index: 9999;
        pointer-events: none;
        text-shadow: 1px 1px 0 #000;
        box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.1);
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
</style>
