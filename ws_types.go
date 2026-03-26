package main

type WSMessageType string

const (
	TypeCommand       WSMessageType = "command"
	TypeEvent         WSMessageType = "event"
	TypeState         WSMessageType = "state"
	TypePlanningError WSMessageType = "planning_error"
	TypeNoOp          WSMessageType = "noop"
)

type CommandPayload struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Target    Target `json:"target"`
	Rationale string `json:"rationale"`
}

type EventPayload struct {
	Event     string `json:"event"`
	Action    string `json:"action"`
	CommandID string `json:"command_id"`
	Cause     string `json:"cause"`
	Duration  int    `json:"duration_ms"`
}

type POI struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Position   Vec3    `json:"position"`
	Distance   float64 `json:"distance"`
	Visibility float64 `json:"visibility"`
	Score      int     `json:"score"`
}

type StatePayload struct {
	Health                 float64  `json:"health"`
	Food                   float64  `json:"food"`
	TimeOfDay              int      `json:"time_of_day"`
	HasBedNearby           bool     `json:"has_bed_nearby"`
	HasCraftingTableNearby bool     `json:"has_crafting_table_nearby"`
	Position               Vec3     `json:"position"`
	Threats                []Threat `json:"threats"`
	POIs                   []POI    `json:"pois"`
	Inventory              []Item   `json:"inventory"`
}

type Threat struct {
	Name string `json:"name"`
}

type Item struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
