<script lang="ts">
    let { name, size = 32 } = $props();

    // Points to the local public folder (e.g., ui/public/textures/apple.png)
    // Vite serves everything in /public at the root path '/'
    const iconUrl = $derived(`/assets/items/${name}.png`);
</script>

<img
    src={iconUrl}
    alt={name}
    width={size}
    height={size}
    style="image-rendering: pixelated; object-fit: contain;"
    onerror={(e) => {
        // Fallback to the classic missing texture checkerboard if the local file is missing
        e.currentTarget.src =
            "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAKElEQVR42mP8z8DAwMgwEAwUMAK1M1Ero7RmcGjAwEBz1DCqGAYAAI8Z/8H/h7i+AAAAAElFTkSuQmCC";
    }}
/>
