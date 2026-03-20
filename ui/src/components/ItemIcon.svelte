<script lang="ts">
    export let name: string;
    export let size: number = 32;

    let hasError = false;

    // Reactively format the name and path in case the prop changes
    $: cleanName = name.replace("minecraft:", "").toLowerCase();
    $: formattedTitle = cleanName.replace(/_/g, " ");
    $: imageSource = `../../public/assets/items/${cleanName}.png`;

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
        on:error={handleError}
        class="mc-icon"
    />
{/if}

<style>
    .mc-icon {
        image-rendering: pixelated; /* Keeps 16x16 pixel art crispy */
        object-fit: contain;
    }

    .fallback-icon {
        background-color: #ff00ff; /* Magenta missing texture vibe */
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
