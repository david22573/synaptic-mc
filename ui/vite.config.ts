import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// https://vite.dev/config/
export default defineConfig({
    plugins: [svelte()],
    server: {
        proxy: {
            "/ui/ws": {
                target: "ws://localhost:8080",
                ws: true,
            },
            "/bot/ws": {
                target: "ws://localhost:8080",
                ws: true,
            },
        },
    },
});
