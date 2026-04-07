<script lang="ts">
    import { botStore, uiStore } from "../lib/store.svelte";

    const health = $derived(botStore.gameState?.health ?? 20);
    const food = $derived(botStore.gameState?.food ?? 20);
    const xpProgress = $derived(botStore.gameState?.experience_progress ?? 0);
    const xpLevel = $derived(botStore.gameState?.level ?? 0);
    const hotbarSlots = $derived(
        botStore.gameState?.hotbar || Array(9).fill(null),
    );
    const offhand = $derived(botStore.gameState?.offhand || null);
    const activeSlot = $derived(botStore.gameState?.active_slot ?? 0);
    
    // Derived state for the warning banner
    const failureWarning = $derived(botStore.activeFailure);

    const fullHearts = $derived(Math.max(0, Math.floor(health / 2)));
    const halfHearts = $derived(health % 2 !== 0 && health > 0 ? 1 : 0);
    const emptyHearts = $derived(Math.max(0, 10 - fullHearts - halfHearts));

    const fullFood = $derived(Math.max(0, Math.floor(food / 2)));
    const halfFood = $derived(food % 2 !== 0 && food > 0 ? 1 : 0);
    const emptyFood = $derived(Math.max(0, 10 - fullFood - halfFood));

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

<div class="mc-overlay">
    <div class="mc-crosshair"></div>

    {#if failureWarning && failureWarning.failure_count > 1}
        <div class="mc-alert-banner" class:critical={failureWarning.failure_count >= 3}>
            <span class="mc-alert-icon">⚠️</span>
            <div class="mc-alert-text">
                <strong>Plan Stalled ({failureWarning.failure_count}/3):</strong> 
                {failureWarning.reason}
            </div>
        </div>
    {/if}

    <div class="mc-hud">
        <div class="mc-bars">
            <div class="mc-bar-half left-align">
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
                    <span class="mc-icon-stat empty">🍗</span>
                {/each}
                {#each Array(halfFood) as _}
                    <span class="mc-icon-stat">🍖</span>
                {/each}
                {#each Array(fullFood) as _}
                    <span class="mc-icon-stat">🍗</span>
                {/each}
            </div>
        </div>

        <div class="mc-xp-bar-container">
            {#if xpLevel > 0}
                <span class="mc-xp-level">{xpLevel}</span>
            {/if}
            <div class="mc-xp-bar">
                <div
                    class="mc-xp-fill"
                    style="width: {xpProgress * 100}%;"
                ></div>
            </div>
        </div>

        <div class="mc-hotbar-container">
            <div
                class="mc-offhand slot"
                role="presentation"
                onmousemove={(e) =>
                    setTooltip(e, offhand ? formatName(offhand.name) : "")}
                onmouseleave={clearTooltip}
            >
                {#if offhand}
                    <img
                        src="/assets/{offhand.name}.png"
                        alt={offhand.name}
                        class="item-icon"
                    />
                    {#if offhand.count > 1}
                        <span class="slot-count">{offhand.count}</span>
                    {/if}
                {/if}
            </div>

            <div class="mc-hotbar">
                {#each hotbarSlots as slot, i}
                    <div
                        class="slot {i === activeSlot ? 'active' : ''}"
                        role="presentation"
                        onmousemove={(e) =>
                            setTooltip(e, slot ? formatName(slot.name) : "")}
                        onmouseleave={clearTooltip}
                    >
                        {#if slot}
                            <img
                                src="/assets/{slot.name}.png"
                                alt={slot.name}
                                class="item-icon"
                            />
                            {#if slot.count > 1}
                                <span class="slot-count">{slot.count}</span>
                            {/if}
                        {/if}
                    </div>
                {/each}
            </div>
        </div>
    </div>
</div>

<style>
    .mc-overlay {
        position: absolute;
        inset: 0;
        pointer-events: none;
        display: flex;
        flex-direction: column;
        justify-content: flex-start;
        align-items: center;
        padding-top: 24px;
        z-index: 10;
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

    .mc-alert-banner {
        background: rgba(40, 0, 0, 0.85);
        border: 2px solid #ff4444;
        color: white;
        padding: 8px 16px;
        margin-bottom: 24px;
        display: flex;
        align-items: center;
        gap: 12px;
        font-family: monospace;
        font-size: 14px;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.5);
        pointer-events: auto;
    }

    .mc-alert-banner.critical {
        background: rgba(80, 0, 0, 0.95);
        border-color: #ff0000;
        animation: pulse 1s infinite alternate;
    }

    .mc-alert-icon {
        font-size: 20px;
    }

    @keyframes pulse {
        from { box-shadow: 0 0 4px #ff0000; }
        to { box-shadow: 0 0 16px #ff0000; }
    }

    .mc-hud {
        display: flex;
        flex-direction: column;
        align-items: center;
    }

    .mc-bars {
        display: flex;
        justify-content: space-between;
        width: 364px;
        gap: 1rem;
        margin-bottom: 6px;
        padding: 0 4px;
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

    .mc-xp-bar-container {
        position: relative;
        width: 364px;
        display: flex;
        justify-content: center;
        margin-bottom: 6px;
    }

    .mc-xp-bar {
        width: 100%;
        height: 10px;
        background: #222;
        border: 2px solid #111;
        box-shadow: inset 1px 1px 0px rgba(0, 0, 0, 0.5);
    }

    .mc-xp-fill {
        height: 100%;
        background: #84e84b;
        box-shadow: inset 0px 2px 0px rgba(255, 255, 255, 0.3);
    }

    .mc-xp-level {
        position: absolute;
        top: -16px;
        color: #84e84b;
        font-family: monospace;
        font-size: 16px;
        font-weight: bold;
        text-shadow:
            2px 2px 0 #000,
            -1px -1px 0 #000,
            1px -1px 0 #000,
            -1px 1px 0 #000;
        z-index: 15;
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
        cursor: help;
        pointer-events: auto;
    }

    .mc-offhand {
        border-color: #373737 #fff #fff #373737;
    }

    .slot.active {
        border-color: #fff;
        box-shadow:
            inset 0 0 0 2px #fff,
            0 0 6px rgba(255, 255, 255, 0.6);
        z-index: 10;
    }

    .item-icon {
        width: 24px;
        height: 24px;
        image-rendering: pixelated;
        pointer-events: none;
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
</style>
