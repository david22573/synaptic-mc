// internal/strategy/evaluator.go
package strategy

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

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

const strategySystemPrompt = `You are the Strategy Director for an autonomous Minecraft survival bot.
Analyze the bot's inventory, health, and progression tier, then output the most appropriate strategic objective.

FULL MINECRAFT TECH TREE (your primary reference):
- Tier 1 WOOD:       Gather logs → planks, sticks, crafting_table, wooden_pickaxe, wooden_sword
- Tier 2 STONE:      Mine cobblestone → stone_pickaxe, stone_sword, furnace
- Tier 3 FOOD:       Hunt animals → smelt meat → 8+ cooked food items stockpiled
- Tier 4 COAL:       Mine coal_ore → fuel for smelting, torches for light
- Tier 5 IRON:       Mine iron_ore (needs stone_pickaxe) → smelt iron_ingots → iron_pickaxe, iron_sword, iron_chestplate
- Tier 6 SHIELD:     Craft shield (iron_ingot + oak_planks) — dramatically improves survivability
- Tier 7 DIAMOND:    Mine diamond_ore at Y=-59 (needs iron_pickaxe) → diamond_pickaxe, diamond_sword, full diamond armor
- Tier 8 ENCHANTING: Gather lapis_lazuli → craft enchanting_table (diamond + obsidian + book) → craft bookshelves → enchant gear
- Tier 9 NETHER:     Craft nether portal (obsidian + flint_and_steel) → enter nether → mine blaze_rods, nether_quartz, ancient_debris
- Tier 10 BREWING:   Craft brewing_stand (blaze_rod + cobblestone) → brew potions for combat
- Tier 11 END:       Locate stronghold via ender_eye → defeat Ender Dragon

RULES:
1. Assess the current tier by inspecting the inventory. The bot is on the highest tier where it has ALL prerequisites.
2. If the current tier is complete, push to the NEXT tier.
3. Survival (food, health) must be addressed before progression if stocks are low.
4. Do not repeat a goal that has clearly already been achieved (e.g. do not say "get wooden pickaxe" if one is in inventory).
5. Be specific — name the exact items to craft, mine, or gather. Avoid vague goals like "progress further".
6. is_autonomous: true only at Tier 7 or above, where the bot should self-direct freely.

OUTPUT FORMAT (strict JSON, no markdown):
{
  "primary_goal": "Specific, actionable primary objective in one sentence.",
  "secondary_goal": "Concrete secondary objective in one sentence.",
  "is_autonomous": false
}`

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
	for k, v := range inv {
		if v > 0 && strings.HasSuffix(k, "_log") {
			hasLog = true
			break
		}
	}

	hasWoodenPick := inv["wooden_pickaxe"] > 0
	hasStonePick := inv["stone_pickaxe"] > 0 || inv["iron_pickaxe"] > 0 || inv["diamond_pickaxe"] > 0
	hasIronPick := inv["iron_pickaxe"] > 0 || inv["diamond_pickaxe"] > 0

	// Tier 1: Wood
	if !hasWoodenPick && !hasStonePick {
		if !hasLog && inv["oak_planks"] == 0 {
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
