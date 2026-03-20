package main

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

type Executor interface {
	Dispatch(action Action) error
	SendControl(msgType, reason string) error
	Close() error
}

type WSExecutor struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func NewWSExecutor(conn *websocket.Conn) *WSExecutor {
	return &WSExecutor{
		conn: conn,
	}
}

func (e *WSExecutor) Dispatch(action Action) error {
	payload, err := json.Marshal(action)
	if err != nil {
		return err
	}
	msg := WSMessage{
		Type:    TypeCommand,
		Payload: json.RawMessage(payload),
	}
	return e.writeJSON(msg)
}

func (e *WSExecutor) SendControl(msgType, reason string) error {
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}
	msg := WSMessage{
		Type:    WSMessageType(msgType),
		Payload: json.RawMessage(payload),
	}
	return e.writeJSON(msg)
}

func (e *WSExecutor) Close() error {
	return e.conn.Close()
}

func (e *WSExecutor) writeJSON(v interface{}) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.conn.WriteJSON(v)
}
