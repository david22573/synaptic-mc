<script lang="ts">
    let { name, size = 32 } = $props<{ name: string; size?: number }>();

    let hasError = $state(false);

    let cleanName = $derived(name.replace("minecraft:", "").toLowerCase());
    let formattedTitle = $derived(cleanName.replace(/_/g, " "));
    let imageSource = $derived(`/assets/items/${cleanName}.png`);

    function handleError() {
        hasError = true;
    }
</script>

{#if hasError}
    <div
        class="fallback-icon"
        style="width: {size}px; height: {size}px; font-size: {size * 0.4}px;"
        title="Missing texture: {cleanName}"
    >
        ?
    </div>
{:else}
    <img
        src={imageSource}
        alt={cleanName}
        title={formattedTitle}
        width={size}
        height={size}
        onerror={handleError}
        class="mc-icon"
    />
{/if}

<style>
    .mc-icon {
        image-rendering: pixelated;
        object-fit: contain;
    }

    .fallback-icon {
        background-color: #ff00ff;
        display: flex;
        align-items: center;
        justify-content: center;
        color: white;
        font-weight: bold;
        box-shadow: inset 0 0 0 2px #000000;
        cursor: help;
        font-family: monospace;
    }
</style>
