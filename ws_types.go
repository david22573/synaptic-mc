package main

import "encoding/json"

type WSMessageType string

const (
	TypeCommand       WSMessageType = "command"
	TypeEvent         WSMessageType = "event"
	TypeState         WSMessageType = "state"
	TypePlanningError WSMessageType = "planning_error"
	TypeNoOp          WSMessageType = "noop"
)

type WSMessage struct {
	Type    WSMessageType   `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type CommandPayload struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Target Target `json:"target"`
}

type EventPayload struct {
	Event     string `json:"event"`
	Action    string `json:"action"`
	CommandID string `json:"command_id"`
	Cause     string `json:"cause"`
	Duration  int    `json:"duration_ms"`
}

type StatePayload struct {
	Health       float64  `json:"health"`
	Food         float64  `json:"food"`
	TimeOfDay    int      `json:"time_of_day"`
	HasBedNearby bool     `json:"has_bed_nearby"`
	Position     Vec3     `json:"position"`
	Threats      []Threat `json:"threats"`
	Inventory    []Item   `json:"inventory"`
}

type Threat struct {
	Name string `json:"name"`
}

type Item struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
