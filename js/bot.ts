import mineflayer from "mineflayer";
import WebSocket from "ws";
import { mineflayer as viewer } from "prismarine-viewer";

// ==========================================
// CONFIGURATION
// ==========================================
const WS_URL = "ws://localhost:8080/ws";
const TICK_RATE_MS = 1000;

// ==========================================
// TYPES
// ==========================================
interface GameState {
    health: number;
    food: number;
    fov_entities: Array<{ type: string; distance: number }>;
}

interface LLMDecision {
    action: "attack" | "retreat" | "mine" | "idle";
    target: string;
    rationale: string;
}

// ==========================================
// INITIALIZATION
// ==========================================
const bot = mineflayer.createBot({
    host: "localhost", // Swap with your server IP if not local
    port: 25565,
    username: "CraftBot",
    version: "1.19", // Must match your server version
});

let ws: WebSocket;
let lastTickTime = 0;
let isThinking = false;

// ==========================================
// WEBSOCKET LOGIC
// ==========================================
function connectControlPlane() {
    ws = new WebSocket(WS_URL);

    ws.on("open", () => {
        console.log("[+] Connected to Go Control Plane.");
    });

    ws.on("message", (data: Buffer) => {
        isThinking = false;

        try {
            const msg = JSON.parse(data.toString());
            if (msg.type === "command") {
                executeDecision(msg.payload as LLMDecision);
            }
        } catch (err) {
            console.error("[-] Failed to parse command from Go:", err);
        }
    });

    ws.on("close", () => {
        console.log("[-] Disconnected from Control Plane. Retrying in 5s...");
        setTimeout(connectControlPlane, 5000);
    });

    ws.on("error", (err) => console.error("WS Error:", err));
}

// ==========================================
// CORE PIPELINE
// ==========================================
function getGameState(): GameState {
    // Filter entities: Only grab mobs/hostiles, sort by distance, take top 5
    const entities = Object.values(bot.entities)
        .filter((e) => e.type === "mob" || e.type === "hostile")
        .map((e) => ({
            type: e.name || "unknown",
            distance: parseFloat(
                bot.entity.position.distanceTo(e.position).toFixed(1),
            ),
        }))
        .sort((a, b) => a.distance - b.distance)
        .slice(0, 5);

    return {
        health: bot.health,
        food: bot.food,
        fov_entities: entities,
    };
}

function executeDecision(decision: LLMDecision) {
    console.log(`\n[>>] Action: ${decision.action.toUpperCase()}`);
    console.log(`[>>] Target: ${decision.target}`);
    console.log(`[>>] Rationale: ${decision.rationale}\n`);

    // Reset movement states before new action
    bot.clearControlStates();

    switch (decision.action) {
        case "attack":
            bot.chat(`Engaging ${decision.target}!`);
            const targetEntity = bot.nearestEntity(
                (e) => e.name === decision.target,
            );
            if (targetEntity) {
                // Look at the entity's head before swinging
                bot.lookAt(
                    targetEntity.position.offset(0, targetEntity.height, 0),
                );
                bot.attack(targetEntity);
            }
            break;

        case "retreat":
            bot.chat("Tactical retreat!");
            bot.setControlState("sprint", true);
            bot.setControlState("forward", true);
            bot.setControlState("jump", true); // Bunny hop away
            break;

        case "idle":
            // Stand still
            break;

        case "mine":
            bot.chat(`Looking for ${decision.target} to mine.`);
            break;
    }
}

// ==========================================
// EVENT LOOP
// ==========================================
bot.once("spawn", () => {
    console.log("[+] Bot spawned in the world.");
    connectControlPlane();

    // Start the web dashboard on port 3000
    viewer(bot, { port: 3000, firstPerson: true });
    console.log("[+] Spectator stream live at http://localhost:3000");
});

bot.on("physicTick", () => {
    if (!ws || ws.readyState !== WebSocket.OPEN) return;

    const now = Date.now();

    // Throttle API pings and prevent overlapping requests
    if (now - lastTickTime < TICK_RATE_MS || isThinking) return;

    const state = getGameState();

    // Cost saving guardrail: Only bother the AI if danger is nearby or health is low
    if (state.fov_entities.length > 0 || state.health < 20) {
        isThinking = true;
        lastTickTime = now;

        ws.send(
            JSON.stringify({
                type: "state",
                payload: state,
            }),
        );
    }
});

bot.on("error", (err) => console.log("Mineflayer Error:", err));
bot.on("kicked", (reason) => console.log("Kicked:", reason));
