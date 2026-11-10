package eval

import (
	"context"
	"reflect"
	"testing"
	"time"

	"summermahjong/riichi/internal/game"
)

// testAgents 是四个合法的内置策略名。
func testAgents() []string {
	return []string{"shanten:attack", "defense:defense", "search:closed", "random"}
}

func fastConfig(seeds ...int64) Config {
	if len(seeds) == 0 {
		seeds = []int64{1, 2}
	}
	return Config{
		Seeds:           seeds,
		Agents:          testAgents(),
		RoundLimit:      1,
		DecisionTimeout: 5 * time.Second, // 足够大，避免超时回退造成不确定性
		Jobs:            2,
		Calls:           CallsPass,
	}
}

func TestConfigWithDefaults(t *testing.T) {
	cfg, err := (Config{Seeds: []int64{1}, Agents: testAgents()}).withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if cfg.RoundLimit != DefaultRoundLimit {
		t.Fatalf("RoundLimit = %d", cfg.RoundLimit)
	}
	if cfg.DecisionTimeout != DefaultDecisionTimeout {
		t.Fatalf("DecisionTimeout = %v", cfg.DecisionTimeout)
	}
	if cfg.Jobs <= 0 {
		t.Fatalf("Jobs 应补默认值: %d", cfg.Jobs)
	}
	if cfg.Calls != CallsPass {
		t.Fatalf("Calls 默认应为 pass，实际 %q", cfg.Calls)
	}
	if cfg.Scorer == nil {
		t.Fatalf("Scorer 默认应为 RulesScorer")
	}
	if cfg.Rules.RoundLimit != cfg.RoundLimit {
		t.Fatalf("Rules.RoundLimit 应与 RoundLimit 同步: %d vs %d", cfg.Rules.RoundLimit, cfg.RoundLimit)
	}
	// 自定义规则不应被覆盖
	custom := Config{
		Seeds:      []int64{1},
		Agents:     testAgents(),
		Rules:      game.Ruleset{StartingPoints: 30000, RoundLimit: 8, RedFives: 3},
		RoundLimit: 2,
	}
	got, err := custom.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults custom: %v", err)
	}
	if got.Rules.StartingPoints != 30000 || got.Rules.RoundLimit != 2 {
		t.Fatalf("自定义规则被覆盖: %+v", got.Rules)
	}
}

func TestConfigWithDefaultsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"无 seeds", Config{Agents: testAgents()}},
		{"agent 数量不对", Config{Seeds: []int64{1}, Agents: []string{"random", "random", "random"}}},
		{"未知 agent", Config{Seeds: []int64{1}, Agents: []string{"random", "random", "random", "nope"}}},
		{"未知 calls", Config{Seeds: []int64{1}, Agents: testAgents(), Calls: CallsPolicy("bogus")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.withDefaults(); err == nil {
				t.Fatalf("期望报错，实际通过")
			}
		})
	}
}

// stripTiming 去掉与机器相关的耗时字段，便于比较可复现性。
func stripTiming(games []GameResult) []GameResult {
	out := make([]GameResult, len(games))
	for i, game := range games {
		game.DurationMs = 0
		game.LatenciesMs = [4][]float64{}
		out[i] = game
	}
	return out
}

func TestRunSameSeedIsDeterministic(t *testing.T) {
	cfg := fastConfig(1, 2, 3)
	first, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("第一次 Run: %v", err)
	}
	second, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("第二次 Run: %v", err)
	}
	if !reflect.DeepEqual(stripTiming(first.Games), stripTiming(second.Games)) {
		t.Fatalf("同一 seed 两次对局结果不一致\n第一次: %+v\n第二次: %+v",
			stripTiming(first.Games), stripTiming(second.Games))
	}
	for seat := 0; seat < 4; seat++ {
		if first.Summary.Seats[seat].Decisions != second.Summary.Seats[seat].Decisions {
			t.Fatalf("座位 %d 决策数不可复现: %d vs %d", seat,
				first.Summary.Seats[seat].Decisions, second.Summary.Seats[seat].Decisions)
		}
		if !reflect.DeepEqual(first.Summary.Seats[seat].WinRate, second.Summary.Seats[seat].WinRate) {
			t.Fatalf("座位 %d 和了率不可复现", seat)
		}
	}
}

func TestRunCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, fastConfig(1, 2, 3, 4)); err == nil {
		t.Fatalf("取消的 context 应当返回错误")
	}
}

// TestRunSmoke100Games 是 100 局 smoke：每场只打一局，验证驱动、指标与 ELO 全链路。
// 加 -short 时跳过（CI 快速路径）。
func TestRunSmoke100Games(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过 100 局 smoke")
	}
	seeds := make([]int64, 100)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	cfg := fastConfig(seeds...)
	cfg.Jobs = 4
	cfg.DecisionTimeout = DefaultDecisionTimeout // 用真实默认值，验证 500ms 超时路径不炸

	result, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("100 局 smoke 出现 %d 场失败: %+v", result.Errors, result.Games)
	}
	if result.Summary.Games != 100 || result.Summary.Rounds != 100 {
		t.Fatalf("Games=%d Rounds=%d，期望 100/100", result.Summary.Games, result.Summary.Rounds)
	}
	if len(result.Summary.Elo.Ratings) != 4 {
		t.Fatalf("ELO 应覆盖 4 个 agent: %+v", result.Summary.Elo)
	}
	totalDecisions := int64(0)
	for seat := 0; seat < 4; seat++ {
		metrics := result.Summary.Seats[seat]
		if metrics.Decisions <= 0 || metrics.Decision.P50Ms < 0 {
			t.Fatalf("座位 %d 指标异常: %+v", seat, metrics)
		}
		if metrics.Rounds != 100 {
			t.Fatalf("座位 %d 轮数 = %d want 100", seat, metrics.Rounds)
		}
		totalDecisions += metrics.Decisions
	}
	if totalDecisions < 400 {
		t.Fatalf("100 局决策数太少: %d", totalDecisions)
	}
}
