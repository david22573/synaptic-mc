// internal/strategy/evaluator.go
package strategy

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

//go:embed prompts/strategy.tmpl
var strategySystemPrompt string

// Directive is the strategic mandate handed to the planner each cycle.
// Unchanged — the planner consumes this as-is.
type Directive struct {
	PrimaryGoal   string
	SecondaryGoal string
	IsAutonomous  bool
}

// LLMClient is the minimal interface this package needs.
type LLMClient interface {
	Generate(ctx context.Context, systemPrompt, userContent string) (string, error)
}

// Evaluator produces a Directive from the current game state.
// If an LLM client is provided, mid-to-late game strategy is delegated to it.
// Survival and night checks always use fast heuristics — no LLM latency there.
type Evaluator struct {
	llm   LLMClient
	cache directiveCache
}

type directiveCache struct {
	mu        sync.Mutex
	directive *Directive
	stateHash uint64
	expiresAt time.Time
}

func NewEvaluator() *Evaluator {
	return &Evaluator{}
}

// NewEvaluatorWithLLM creates an evaluator backed by an LLM for progression strategy.
// Call this from main.go instead of NewEvaluator().
func NewEvaluatorWithLLM(client LLMClient) *Evaluator {
	return &Evaluator{llm: client}
}

// Evaluate returns the current strategic directive.
// Priority: survival > night safety > LLM progression > heuristic fallback.
func (e *Evaluator) Evaluate(state domain.GameState) Directive {
	// 1. Survival is always heuristic — fast, no LLM.
	if d, ok := e.survivalCheck(state); ok {
		return d
	}

	// 2. Nightfall is always heuristic — time-sensitive.
	if d, ok := e.nightCheck(state); ok {
		return d
	}

	// 3. Delegate progression strategy to the LLM.
	if e.llm != nil {
		if d, ok := e.cachedLLMDirective(state); ok {
			return d
		}
	}

	// 4. Heuristic fallback if LLM is unavailable or fails.
	return e.heuristicProgression(state)
}

// ─────────────────────────────────────────────────────────────────
// Fast heuristic checks (no LLM, no latency)
// ─────────────────────────────────────────────────────────────────

func (e *Evaluator) survivalCheck(state domain.GameState) (Directive, bool) {
	hasFood := false
	for _, item := range state.Inventory {
		if isFood(item.Name) {
			hasFood = true
			break
		}
	}

	if state.Health < 10 || state.Food < 6 {
		if hasFood {
			return Directive{
				PrimaryGoal:   "SURVIVAL: Eat immediately — food is in inventory. Use the 'eat' action to regenerate health.",
				SecondaryGoal: "DEFENSE: Retreat to safety while healing.",
			}, true
		}
		return Directive{
			PrimaryGoal:   "SURVIVAL: Starving with no food. Cannot hunt — health too low. Use 'gather' for berries or apples, or 'explore' for a village.",
			SecondaryGoal: "DEFENSE: Avoid all combat. Retreat from threats.",
		}, true
	}
	return Directive{}, false
}

func (e *Evaluator) nightCheck(state domain.GameState) (Directive, bool) {
	isNight := state.TimeOfDay > 12541 && state.TimeOfDay < 23000
	if isNight && !state.HasBedNearby {
		return Directive{
			PrimaryGoal:   "SHELTER: Survive the night. Avoid open areas. Dig a 3-block hole and cover the top, or find an existing structure.",
			SecondaryGoal: "TECH: While sheltered, craft or smelt with available materials.",
		}, true
	}
	return Directive{}, false
}

// ─────────────────────────────────────────────────────────────────
// LLM-driven progression directive
// ─────────────────────────────────────────────────────────────────

// directiveResponse is what the LLM returns.
type directiveResponse struct {
	PrimaryGoal   string `json:"primary_goal"`
	SecondaryGoal string `json:"secondary_goal"`
	IsAutonomous  bool   `json:"is_autonomous"`
}

// cachedLLMDirective returns a cached directive if the state hash and TTL are still valid,
// otherwise calls the LLM and caches the result for 45 seconds.
// 45 seconds is long enough to avoid thrashing but short enough to react to major state changes.
func (e *Evaluator) cachedLLMDirective(state domain.GameState) (Directive, bool) {
	h := hashGameStateForDirective(state)

	e.cache.mu.Lock()
	defer e.cache.mu.Unlock()

	if e.cache.directive != nil && e.cache.stateHash == h && time.Now().Before(e.cache.expiresAt) {
		return *e.cache.directive, true
	}

	d, ok := e.llmDirective(state)
	if !ok {
		return Directive{}, false
	}

	e.cache.directive = &d
	e.cache.stateHash = h
	e.cache.expiresAt = time.Now().Add(45 * time.Second)
	return d, true
}

func (e *Evaluator) llmDirective(state domain.GameState) (Directive, bool) {
	userContent := buildStrategyContext(state)

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	raw, err := e.llm.Generate(ctx, strategySystemPrompt, userContent)
	if err != nil {
		return Directive{}, false
	}

	raw = domain.CleanJSON(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var resp directiveResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return Directive{}, false
	}
	if resp.PrimaryGoal == "" {
		return Directive{}, false
	}

	return Directive{
		PrimaryGoal:   resp.PrimaryGoal,
		SecondaryGoal: resp.SecondaryGoal,
		IsAutonomous:  resp.IsAutonomous,
	}, true
}

// buildStrategyContext formats the state into a compact LLM-readable summary.
func buildStrategyContext(state domain.GameState) string {
	inv := make(map[string]int)
	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
	}

	var invParts []string
	for name, count := range inv {
		invParts = append(invParts, name+":"+itoa(count))
	}

	var sb strings.Builder
	sb.WriteString("CURRENT STATE:\n")
	sb.WriteString("Health: " + ftoa(state.Health) + "/20\n")
	sb.WriteString("Food: " + ftoa(state.Food) + "/20\n")
	sb.WriteString("Position: Y=" + ftoa(state.Position.Y) + "\n")
	sb.WriteString("Time: " + itoa(int(state.TimeOfDay)) + " (day < 12541)\n")
	sb.WriteString("Inventory: " + strings.Join(invParts, ", ") + "\n")

	if len(state.POIs) > 0 {
		var pois []string
		for _, p := range state.POIs {
			pois = append(pois, p.Name)
		}
		sb.WriteString("Nearby POIs: " + strings.Join(pois, ", ") + "\n")
	}

	sb.WriteString("\nDetermine the current progression tier and output the next strategic objective.")
	return sb.String()
}

// ─────────────────────────────────────────────────────────────────
// Heuristic fallback — used when LLM is nil or unavailable
// ─────────────────────────────────────────────────────────────────

func (e *Evaluator) heuristicProgression(state domain.GameState) Directive {
	inv := make(map[string]int)
	hasWeapon := false
	hasFood := false
	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
		if strings.Contains(item.Name, "sword") || strings.Contains(item.Name, "axe") {
			hasWeapon = true
		}
		if isFood(item.Name) {
			hasFood = true
		}
	}
	_ = hasFood

	hasLog := false
	hasPlanks := false
	for k, v := range inv {
		if v > 0 {
			if strings.HasSuffix(k, "_log") {
				hasLog = true
			}
			if strings.HasSuffix(k, "_planks") {
				hasPlanks = true
			}
		}
	}

	hasWoodenPick := inv["wooden_pickaxe"] > 0
	hasStonePick := inv["stone_pickaxe"] > 0 || inv["iron_pickaxe"] > 0 || inv["diamond_pickaxe"] > 0
	hasIronPick := inv["iron_pickaxe"] > 0 || inv["diamond_pickaxe"] > 0

	// Tier 1: Wood
	if !hasWoodenPick && !hasStonePick {
		if !hasLog && !hasPlanks {
			return Directive{PrimaryGoal: "TECH TIER 1 (Wood): Gather logs — this is the absolute first step.", SecondaryGoal: "Note stone and coal locations for the next tier."}
		}
		return Directive{PrimaryGoal: "TECH TIER 1 (Tools): Craft planks, sticks, crafting_table, wooden_pickaxe.", SecondaryGoal: "Gather more wood if near trees."}
	}
	// Tier 2: Stone
	if !hasStonePick {
		return Directive{PrimaryGoal: "TECH TIER 2 (Stone): Mine 3+ cobblestone with wooden_pickaxe, craft stone_pickaxe.", SecondaryGoal: "Craft a stone sword immediately after."}
	}
	// Armament
	if !hasWeapon && (inv["cobblestone"] >= 2 || inv["stone"] >= 2) {
		return Directive{PrimaryGoal: "ARMAMENT: Craft a stone sword or stone axe.", SecondaryGoal: "Mine coal if spotted."}
	}
	// Tier 3: Food
	cookedFood := 0
	for k, v := range inv {
		if strings.HasPrefix(k, "cooked_") || k == "bread" || k == "baked_potato" {
			cookedFood += v
		}
	}
	if cookedFood < 5 {
		return Directive{PrimaryGoal: "SUSTENANCE: Hunt and smelt meat until you have 5+ cooked food items.", SecondaryGoal: "Maintain tools."}
	}
	// Tier 4: Coal
	if inv["coal"] == 0 {
		return Directive{PrimaryGoal: "RESOURCES: Find and mine coal_ore — needed for smelting and torches.", SecondaryGoal: "Note iron_ore locations."}
	}
	// Tier 5: Iron
	if !hasIronPick {
		return Directive{PrimaryGoal: "IRON TIER: Mine iron_ore (needs stone_pickaxe), smelt iron_ingots, craft iron_pickaxe.", SecondaryGoal: "Maintain food and coal stockpiles."}
	}
	// Tier 6+: Autonomous
	return Directive{
		PrimaryGoal:   "AUTONOMY: Core needs met. Evaluate inventory and world knowledge. Consider mining diamonds (Y=-59), building a base, or trading with villagers.",
		SecondaryGoal: "Keep food above 10 and tools in good repair.",
		IsAutonomous:  true,
	}
}

// ─────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────

func isFood(name string) bool {
	foods := []string{
		"beef", "porkchop", "mutton", "chicken", "rabbit",
		"cooked_beef", "cooked_porkchop", "cooked_mutton", "cooked_chicken", "cooked_rabbit",
		"apple", "sweet_berries", "bread", "carrot", "potato", "baked_potato",
		"kelp", "dried_kelp",
	}
	for _, f := range foods {
		if strings.Contains(name, f) {
			return true
		}
	}
	return false
}

// hashGameStateForDirective produces a coarse hash of progression-relevant fields.
// We don't need to re-ask the LLM every tick — only when the tier likely changed.
func hashGameStateForDirective(state domain.GameState) uint64 {
	inv := make(map[string]int)
	for _, item := range state.Inventory {
		inv[item.Name] += item.Count
	}
	// Hash a small set of progression-sentinel items + rough health/food bands.
	sentinels := []string{
		"wooden_pickaxe", "stone_pickaxe", "iron_pickaxe", "diamond_pickaxe",
		"iron_sword", "diamond_sword", "iron_chestplate", "diamond_chestplate",
		"enchanting_table", "blaze_rod", "obsidian", "coal", "iron_ingot", "diamond",
	}
	var h uint64 = 14695981039346656037
	for _, s := range sentinels {
		count := inv[s]
		h ^= uint64(count)
		h *= 1099511628211
	}
	// Band health and food coarsely so small fluctuations don't bust the cache.
	h ^= uint64(int(state.Health/5)) * 1099511628211
	h ^= uint64(int(state.Food/5)) * 1099511628211
	return h
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func ftoa(f float64) string {
	return itoa(int(f))
}
