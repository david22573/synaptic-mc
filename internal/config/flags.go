package config

type FeatureFlags struct {
	AsyncLoop       bool `json:"async_loop" env:"FF_ASYNC_LOOP"`
	NonBlockPlanner bool `json:"non_block_planner" env:"FF_NON_BLOCK_PLANNER"`
	ActionQueue     bool `json:"action_queue" env:"FF_ACTION_QUEUE"`
	ClientSmooth    bool `json:"client_smooth" env:"FF_CLIENT_SMOOTH"`
}

func DefaultFlags() FeatureFlags {
	return FeatureFlags{
		AsyncLoop:       false,
		NonBlockPlanner: false,
		ActionQueue:     false,
		ClientSmooth:    false,
	}
}
