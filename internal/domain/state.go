package domain

type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type GameState struct {
	Health       float64  `json:"health"`
	Food         float64  `json:"food"`
	TimeOfDay    int      `json:"time_of_day"`
	HasBedNearby bool     `json:"has_bed_nearby"`
	Position     Vec3     `json:"position"`
	Threats      []Threat `json:"threats"`
	POIs         []POI    `json:"pois"`
	Inventory    []Item   `json:"inventory"`
}

type Threat struct {
	Name string `json:"name"`
}

type POI struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Position   Vec3    `json:"position"`
	Distance   float64 `json:"distance"`
	Visibility float64 `json:"visibility"`
	Score      int     `json:"score"`
	Direction  string  `json:"direction"`
}

type Item struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Action struct {
	ID        string       `json:"id"`
	Source    string       `json:"source"`
	Trace     TraceContext `json:"trace"`
	Action    string       `json:"action"`
	Target    Target       `json:"target"`
	Rationale string       `json:"rationale"`
	Priority  int          `json:"priority"`
}

type Target struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type Plan struct {
	Objective string   `json:"objective"`
	Tasks     []Action `json:"tasks"`
}
