package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

// ==========================================
// CONFIGURATION
// ==========================================
const (
	Port          = ":8080"
	OpenRouterURL = "https://openrouter.ai/api/v1/chat/completions"
	ModelName     = "mistralai/mistral-small-2603"
)

// ==========================================
// DATA CONTRACTS (JS <-> Go)
// ==========================================
type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type LLMDecision struct {
	Action    string `json:"action"`
	Target    string `json:"target"`
	Rationale string `json:"rationale"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // Allow local connections
}

// ==========================================
// WEBSOCKET HANDLER
// ==========================================
func handleBotConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("[-] WebSocket upgrade failed:", err)
		return
	}
	defer conn.Close()

	log.Println("[+] Bot connected to control plane.")

	// Mutex to prevent concurrent writes to the WebSocket
	var writeMutex sync.Mutex

	for {
		var msg WSMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("[-] Bot disconnected or read error:", err)
			break
		}

		if msg.Type == "state" {
			// Spawn a goroutine to handle the API call so we don't block reading
			go func(statePayload []byte) {
				decision, err := queryMistralAPI(statePayload)
				if err != nil {
					log.Println("[-] AI Engine Error:", err)
					return
				}

				// Pack and send the command back to JS
				responseMsg := WSMessage{
					Type: "command",
				}
				responseMsg.Payload, _ = json.Marshal(decision)

				writeMutex.Lock()
				err = conn.WriteJSON(responseMsg)
				writeMutex.Unlock()

				if err != nil {
					log.Println("[-] Failed to send command to bot:", err)
				}
			}(msg.Payload)
		}
	}
}

// ==========================================
// AI ENGINE
// ==========================================
func queryMistralAPI(gameState []byte) (*LLMDecision, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY environment variable is not set")
	}

	systemPrompt := `You are a headless Minecraft survival bot. Analyze the game state and output your decision.
You must output a raw JSON object matching this exact schema:
{
  "action": "attack" | "retreat" | "mine" | "idle",
  "target": "string (entity_name, block_name, or none)",
  "rationale": "string (1 sentence explaining why)"
}`

	// Build the OpenRouter payload
	payload := map[string]interface{}{
		"model": ModelName,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(gameState)},
		},
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0.1,
		"max_tokens":      150,
	}

	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", OpenRouterURL, bytes.NewBuffer(jsonPayload))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("HTTP-Referer", "http://localhost:8080")
	req.Header.Set("X-Title", "CraftD Bot Controller")

	client := &http.Client{Timeout: 10 * time.Second}
	startTime := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	choices := result["choices"].([]interface{})
	message := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	content := message["content"].(string)

	log.Printf("[+] API latency: %v | Raw: %s\n", time.Since(startTime), content)

	var decision LLMDecision
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return nil, fmt.Errorf("failed to parse structured JSON: %v", err)
	}

	return &decision, nil
}

func main() {
	// Load environment variables from .env
	err := godotenv.Load()
	if err != nil {
		log.Println("[!] Warning: No .env file found. Falling back to system environment variables.")
	} else {
		log.Println("[+] Loaded variables from .env file.")
	}

	http.HandleFunc("/ws", handleBotConnection)
	log.Printf("CraftD Control Plane listening on ws://localhost%s/ws\n", Port)
	log.Fatal(http.ListenAndServe(Port, nil))
}
