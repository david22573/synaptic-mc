package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type UIHub struct {
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
	logger  *slog.Logger
}

func NewUIHub(logger *slog.Logger) *UIHub {
	return &UIHub{
		clients: make(map[*websocket.Conn]bool),
		logger:  logger,
	}
}

func (h *UIHub) HandleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("UI Hub upgrade failed", slog.Any("error", err))
		return
	}

	h.mu.Lock()
	h.clients[ws] = true
	h.mu.Unlock()

	h.logger.Info("UI Client connected")

	// Read loop to detect disconnects
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.clients, ws)
			h.mu.Unlock()
			ws.Close()
			h.logger.Info("UI Client disconnected")
		}()
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

func (h *UIHub) Broadcast(msg interface{}) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for client := range h.clients {
		err := client.WriteMessage(websocket.TextMessage, payload)
		if err != nil {
			client.Close()
			delete(h.clients, client)
		}
	}
}
