<script lang="ts">
    import { botStore, uiStore } from "../lib/store.svelte";
    import ItemIcon from "./ItemIcon.svelte";

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
    const hasHalfHeart = $derived(health % 2 >= 1);
    const emptyHearts = $derived(Math.max(0, 10 - fullHearts - (hasHalfHeart ? 1 : 0)));

    const fullFood = $derived(Math.max(0, Math.floor(food / 2)));
    const hasHalfFood = $derived(food % 2 >= 1);
    const emptyFood = $derived(Math.max(0, 10 - fullFood - (hasHalfFood ? 1 : 0)));

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

    <div class="mc-hud-container">
        <div class="mc-bars">
            <div class="mc-bar health-bar">
                {#each Array(emptyHearts) as _}
                    <div class="icon-container empty"><div class="heart empty"></div></div>
                {/each}
                {#if hasHalfHeart}
                    <div class="icon-container"><div class="heart half"></div></div>
                {/if}
                {#each Array(fullHearts) as _}
                    <div class="icon-container"><div class="heart full"></div></div>
                {/each}
            </div>

            <div class="mc-bar food-bar">
                {#each Array(fullFood) as _}
                    <div class="icon-container"><div class="food full"></div></div>
                {/each}
                {#if hasHalfFood}
                    <div class="icon-container"><div class="food half"></div></div>
                {/if}
                {#each Array(emptyFood) as _}
                    <div class="icon-container empty"><div class="food empty"></div></div>
                {/each}
            </div>
        </div>

        <div class="mc-xp-container">
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

        <div class="mc-hotbar-outer">
            <div
                class="mc-offhand slot"
                role="presentation"
                onmousemove={(e) =>
                    setTooltip(e, offhand ? formatName(offhand.name) : "")}
                onmouseleave={clearTooltip}
            >
                {#if offhand}
                    <ItemIcon name={offhand.name} size={24} />
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
                            <ItemIcon name={slot.name} size={24} />
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
        justify-content: flex-end;
        align-items: center;
        padding-bottom: 8px;
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
        position: absolute;
        top: 80px;
        background: rgba(40, 0, 0, 0.85);
        border: 2px solid #ff4444;
        color: white;
        padding: 8px 16px;
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

    @keyframes pulse {
        from { box-shadow: 0 0 4px #ff0000; }
        to { box-shadow: 0 0 16px #ff0000; }
    }

    .mc-hud-container {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 2px;
    }

    .mc-bars {
        display: flex;
        justify-content: space-between;
        width: 364px;
        padding: 0 2px;
        margin-bottom: 1px;
    }

    .mc-bar {
        display: flex;
        gap: 0px;
    }

    .health-bar {
        flex-direction: row-reverse;
    }

    .icon-container {
        width: 18px;
        height: 18px;
        display: flex;
        justify-content: center;
        align-items: center;
    }

    /* CSS Heart */
    .heart {
        width: 9px;
        height: 9px;
        background: #ff0000;
        position: relative;
        transform: rotate(-45deg);
        box-shadow: -1px -1px 0 #000, 1px 1px 0 #000;
    }
    .heart::before, .heart::after {
        content: "";
        width: 9px;
        height: 9px;
        background: #ff0000;
        border-radius: 50%;
        position: absolute;
    }
    .heart::before { top: -5px; left: 0; }
    .heart::after { left: 5px; top: 0; }

    .heart.half { background: linear-gradient(to right, #ff0000 50%, #333 50%); }
    .heart.half::after { background: #333; }
    .heart.empty { background: #333; }
    .heart.empty::before, .heart.empty::after { background: #333; }

    /* CSS Food Icon (Drumstick-ish) */
    .food {
        width: 10px;
        height: 12px;
        background: #964b00;
        border-radius: 4px 4px 2px 2px;
        position: relative;
        box-shadow: 1px 1px 0 #000;
    }
    .food::after {
        content: "";
        position: absolute;
        bottom: -4px;
        left: 2px;
        width: 6px;
        height: 6px;
        background: #c68642;
        border-radius: 50%;
    }
    .food.half { background: linear-gradient(to bottom, #964b00 50%, #333 50%); }
    .food.empty { background: #333; }
    .food.empty::after { background: #222; }

    .mc-xp-container {
        position: relative;
        width: 364px;
        height: 12px;
        display: flex;
        flex-direction: column;
        align-items: center;
        margin-top: -2px;
    }

    .mc-xp-bar {
        width: 100%;
        height: 5px;
        background: #111;
        border: 1px solid #000;
    }

    .mc-xp-fill {
        height: 100%;
        background: #84e84b;
    }

    .mc-xp-level {
        position: absolute;
        top: -14px;
        color: #84e84b;
        font-family: monospace;
        font-size: 14px;
        font-weight: bold;
        text-shadow: 1px 1px 0 #000, -1px -1px 0 #000, 1px -1px 0 #000, -1px 1px 0 #000;
        z-index: 15;
    }

    .mc-hotbar-outer {
        display: flex;
        align-items: flex-end;
        gap: 4px;
        margin-top: 2px;
    }

    .mc-hotbar {
        display: flex;
        background: rgba(0, 0, 0, 0.4);
        border: 2px solid #111;
        padding: 1px;
    }

    .slot {
        width: 36px;
        height: 36px;
        background: rgba(139, 139, 139, 0.5);
        border: 2px solid #373737;
        position: relative;
        display: flex;
        justify-content: center;
        align-items: center;
        cursor: help;
        pointer-events: auto;
    }

    .slot.active {
        border: 2px solid #fff;
        background: rgba(255, 255, 255, 0.2);
        outline: 2px solid #000;
        z-index: 10;
    }

    .slot-count {
        position: absolute;
        bottom: 1px;
        right: 2px;
        font-family: monospace;
        font-size: 14px;
        font-weight: bold;
        color: white;
        text-shadow: 1px 1px 0 #3f3f3f;
    }
</style>

