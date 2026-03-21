package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const writeWait = 10 * time.Second

type UIClient struct {
	hub  *UIHub
	conn *websocket.Conn
	send chan []byte
}

type UIHub struct {
	clients    map[*UIClient]bool
	broadcast  chan []byte
	register   chan *UIClient
	unregister chan *UIClient
	logger     *slog.Logger
}

func NewUIHub(logger *slog.Logger) *UIHub {
	return &UIHub{
		broadcast:  make(chan []byte, 256),
		register:   make(chan *UIClient),
		unregister: make(chan *UIClient),
		clients:    make(map[*UIClient]bool),
		logger:     logger.With(slog.String("component", "ui_hub")),
	}
}

func (h *UIHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			h.logger.Info("UI Client connected", slog.Int("active_clients", len(h.clients)))
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.logger.Info("UI Client disconnected", slog.Int("active_clients", len(h.clients)))
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

func (h *UIHub) Broadcast(msg interface{}) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.broadcast <- payload
}

func (h *UIHub) HandleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("UI Hub upgrade failed", slog.Any("error", err))
		return
	}

	client := &UIClient{hub: h, conn: ws, send: make(chan []byte, 256)}
	h.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *UIClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (c *UIClient) writePump() {
	defer func() {
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		}
	}
}
