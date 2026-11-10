// Package eval 是 riichi 的自对弈评测框架（S9）。
//
// 设计目标：
//   - **进程内自对弈**：不走网络，直接 game.New/Apply 驱动四个 agent 打完整半荘；
//   - **通用驱动**：只依赖 Observation.LegalActions / Responses，不假设动作种类
//     （摸打、吃碰杠、立直、和了都由引擎给合法集合，agent 从集合里选）；
//   - **可复现**：同一 seed + 同一 agent 配置 → 同一动作序列与同一结果；
//   - **可出报告**：每座位指标（和了率/放铳率/平均顺位/平均打点/立直率/副露率/
//     决策耗时 P50/P95/役满数）+ 95% 置信区间 + Bradley-Terry ELO + manifest。
//
// 计分：默认注入 RulesScorer（rules 包的真实役/符/点）。rules.Decompose 目前只支持
// 门清手牌，副露和了会回退成「1 翻 30 符」的近似结算并计入报告近似占比（见 scorer.go）。
package eval

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
)

// 默认参数（与 CLI 默认值保持一致）。
const (
	// DefaultDecisionTimeout 是单次决策上限（契约 §4：默认 500ms，超时回退首选）。
	DefaultDecisionTimeout = 500 * time.Millisecond
	// DefaultRoundLimit 是半荘局数上限（东 1-4 + 南 1-4）。
	DefaultRoundLimit = 8
	// maxActionsPerGame 是单局动作上限，防止引擎 bug 导致死循环。
	maxActionsPerGame = 20000
)

// CallsPolicy 控制响应窗口（吃/碰/杠）的决策方式。
type CallsPolicy string

const (
	// CallsAgent 让 agent 自己决定响应（吃/碰/杠/荣和/pass 都问 agent）。
	//
	// 注意：S5 的「吃/碰后打牌」流程尚未接通（鸣牌后 Legal() 为空），
	// 该策略目前会让对局 aborted；等 S5 修好后可直接启用。
	CallsAgent CallsPolicy = "agent"
	// CallsPass 只取荣和，其余一律 pass（得到全门清对局，计分精确，是默认策略）。
	CallsPass CallsPolicy = "pass"
)

// Config 是一次评测的配置。
type Config struct {
	// Seeds 是要跑的 seed 列表。
	Seeds []int64
	// Agents 是四个座位的 agent 名字（agent.New 支持的名字，如 "shanten:attack"）。
	Agents []string
	// Rules 是对局规则；零值时用 game.DefaultRuleset()。
	Rules game.Ruleset
	// RoundLimit 是半荘局数上限；<=0 时用 DefaultRoundLimit。
	RoundLimit int
	// DecisionTimeout 是单次决策上限；<=0 时用 DefaultDecisionTimeout。
	DecisionTimeout time.Duration
	// Jobs 是并发跑不同 seed 的 goroutine 数；<=0 时用 CPU 数（上限 4）。
	Jobs int
	// Calls 是响应窗口策略；空串按 CallsAgent 处理。
	Calls CallsPolicy
	// Scorer 是计分实现；nil 时用 RulesScorer{}。
	Scorer game.ScoreProvider
}

// withDefaults 补齐零值并校验。
func (c Config) withDefaults() (Config, error) {
	if len(c.Seeds) == 0 {
		return c, errors.New("eval: seeds 不能为空")
	}
	if len(c.Agents) != 4 {
		return c, fmt.Errorf("eval: agents 必须恰好 4 个，实际 %d 个", len(c.Agents))
	}
	for seat, name := range c.Agents {
		if _, err := agent.New(name, agent.Options{}); err != nil {
			return c, fmt.Errorf("eval: 座位 %d 的 agent %q 无效: %w", seat, name, err)
		}
	}
	if c.Rules.StartingPoints <= 0 || c.Rules.RoundLimit <= 0 {
		defaults := game.DefaultRuleset()
		if c.Rules.StartingPoints <= 0 {
			c.Rules.StartingPoints = defaults.StartingPoints
		}
		if c.Rules.RoundLimit <= 0 {
			c.Rules.RoundLimit = defaults.RoundLimit
		}
	}
	if c.RoundLimit <= 0 {
		c.RoundLimit = DefaultRoundLimit
	}
	c.Rules.RoundLimit = c.RoundLimit
	if c.DecisionTimeout <= 0 {
		c.DecisionTimeout = DefaultDecisionTimeout
	}
	if c.Jobs <= 0 {
		c.Jobs = min(runtime.NumCPU(), 4)
	}
	if c.Calls == "" {
		c.Calls = CallsPass
	}
	if c.Calls != CallsAgent && c.Calls != CallsPass {
		return c, fmt.Errorf("eval: 未知 calls 策略 %q（支持 %q / %q）", c.Calls, CallsAgent, CallsPass)
	}
	if c.Scorer == nil {
		c.Scorer = RulesScorer{}
	}
	return c, nil
}

// SeatRoundStats 是单个座位在一场对局内的累计计数（可直接 JSON 序列化）。
type SeatRoundStats struct {
	Decisions int64 `json:"decisions"`
	Wins      int64 `json:"wins"`
	TsumoWins int64 `json:"tsumo_wins"`
	RonWins   int64 `json:"ron_wins"`
	Deals     int64 `json:"deals"` // 放铳次数
	Riichi    int64 `json:"riichi"`
	Melds     int64 `json:"melds"` // 吃/碰/明杠/加杠（不含暗杠）
	Ankan     int64 `json:"ankan"`
	OpenWins  int64 `json:"open_wins"` // 副露和了次数（计分走近似，报告单独标注）
	Yakuman   int64 `json:"yakuman"`
	WinPoints int64 `json:"win_points"` // 和了总点数（自摸为三家支付之和）
}

// GameResult 是一场对局（默认一整个半荘）的结果。
type GameResult struct {
	Seed        int64             `json:"seed"`
	Rounds      int               `json:"rounds"`
	Actions     int               `json:"actions"`
	FinalScores [4]int            `json:"final_scores"`
	Ranks       [4]int            `json:"ranks"` // 1..4；同分并列（1+高于自己的座位数）
	Seats       [4]SeatRoundStats `json:"seats"`
	Aborted     bool              `json:"aborted,omitempty"`
	Error       string            `json:"error,omitempty"`
	DurationMs  float64           `json:"duration_ms"`

	// LatenciesMs 只用于汇总 P50/P95，不写进 report.json（体积原因）。
	LatenciesMs [4][]float64 `json:"-"`
}

// Result 是一次评测的全部产物。
type Result struct {
	Config    Config
	Games     []GameResult
	Summary   Summary
	GamesRan  int
	Errors    int
	StartedAt time.Time
	Duration  time.Duration
}

// Summary 是跨 seed 的汇总指标。
type Summary struct {
	Seats    [4]SeatMetrics `json:"seats"`
	Elo      EloResult      `json:"elo"`
	Rounds   int64          `json:"rounds"`
	Games    int64          `json:"games"`
	ErrorN   int64          `json:"error_games"`
	Duration DurationStat   `json:"duration_ms"`
}

// Run 按配置并发跑完所有 seed 并汇总指标。
//
// 单个 seed 的对局失败不会中断整体评测：错误记录在 GameResult.Error 并计入
// Summary.ErrorN，方便在报告里看到「引擎/驱动异常率」。
func Run(ctx context.Context, cfg Config) (*Result, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}

	started := time.Now()
	games := make([]GameResult, len(cfg.Seeds))
	workers := cfg.Jobs
	if workers > len(cfg.Seeds) {
		workers = len(cfg.Seeds)
	}
	seedCh := make(chan int, len(cfg.Seeds))
	for i := range cfg.Seeds {
		seedCh <- i
	}
	close(seedCh)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range seedCh {
				if ctx.Err() != nil {
					return
				}
				games[index] = playGame(cfg, cfg.Seeds[index])
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := &Result{
		Config:    cfg,
		Games:     games,
		GamesRan:  len(games),
		StartedAt: started,
		Duration:  time.Since(started),
	}
	result.Summary = summarize(cfg, games)
	for _, g := range games {
		if g.Error != "" {
			result.Errors++
		}
	}
	return result, nil
}

// playGame 用一个 seed 打一场对局（默认半荘），返回可汇总的结果。
func playGame(cfg Config, seed int64) GameResult {
	res := GameResult{Seed: seed, Ranks: [4]int{1, 2, 3, 4}}
	started := time.Now()

	agents := make([]agent.Agent, 4)
	for seat, name := range cfg.Agents {
		a, err := agent.New(name, agent.Options{Seed: seed*4 + int64(seat)})
		if err != nil {
			res.Error = fmt.Sprintf("构造 agent 座位 %d（%s）失败: %v", seat, name, err)
			res.DurationMs = float64(time.Since(started).Microseconds()) / 1000
			return res
		}
		agents[seat] = a
	}

	state, err := game.NewWithOptions(seed, cfg.Rules, game.Options{
		ManualResponses: true,
		ScoreProvider:   cfg.Scorer,
	})
	if err != nil {
		res.Error = fmt.Sprintf("建局失败: %v", err)
		res.DurationMs = float64(time.Since(started).Microseconds()) / 1000
		return res
	}

	eventCursor := 0
	actions := 0
	for {
		if state.GameOver() {
			break
		}
		if actions > maxActionsPerGame {
			res.Aborted = true
			res.Error = fmt.Sprintf("单局动作数超过上限 %d（疑似引擎未推进）", maxActionsPerGame)
			break
		}
		switch state.Phase {
		case game.PhaseAwaitDraw:
			legal := state.Legal()
			if len(legal) == 0 {
				res.Aborted = true
				res.Error = "await_draw 阶段没有合法动作"
			} else if err := state.Apply(legal[0]); err != nil {
				// 牌山摸空会返回错误，但状态已经完成荒牌流局结算；
				// 其它错误才是真的驱动异常。
				if state.Phase != game.PhaseRoundEnd && state.Phase != game.PhaseGameEnd {
					res.Aborted = true
					res.Error = fmt.Sprintf("摸牌失败: %v", err)
				}
			}
			actions++

		case game.PhaseAwaitDiscard:
			seat := state.Turn
			obs := state.Observe(seat)
			action, err := decideOne(agents[seat], obs, seat, cfg.DecisionTimeout, &res)
			if err != nil {
				res.Aborted = true
				res.Error = fmt.Sprintf("座位 %d 决策失败: %v", seat, err)
				break
			}
			// 自摸和了强制取：S8 内置策略只会输出打牌动作，但和了恒优于继续打牌，
			// 由驱动层补上（与 S6 服务端「和了优先」的约定一致）。
			action = forceWin(obs, action)
			if err := state.Apply(action); err != nil {
				res.Aborted = true
				res.Error = fmt.Sprintf("座位 %d 应用动作 %v 失败: %v", seat, action.Kind, err)
				break
			}
			actions++

		case game.PhaseAwaitResponse:
			acted := false
			for seat := game.Seat(0); seat < 4; seat++ {
				responses := state.Responses(seat)
				if len(responses) == 0 {
					continue
				}
				action, err := decideResponse(agents[seat], state, seat, responses, cfg, &res)
				if err != nil {
					res.Aborted = true
					res.Error = fmt.Sprintf("座位 %d 响应决策失败: %v", seat, err)
					break
				}
				if err := state.Apply(action); err != nil {
					res.Aborted = true
					res.Error = fmt.Sprintf("座位 %d 应用响应 %v 失败: %v", seat, action.Kind, err)
					break
				}
				acted = true
				actions++
			}
			if res.Error != "" {
				break
			}
			// 理论上所有可响应座位都会被问到并结算；这里兜底防止 WIP 引擎卡住。
			if !acted && state.Phase == game.PhaseAwaitResponse {
				if err := state.ResolveTimeout(); err != nil {
					res.Aborted = true
					res.Error = fmt.Sprintf("响应结算失败: %v", err)
				}
			}

		case game.PhaseRoundEnd:
			tallyRound(&res, state, eventCursor)
			eventCursor = len(state.Events)
			res.Rounds++
			if err := state.StartNextRound(); err != nil {
				res.Aborted = true
				res.Error = fmt.Sprintf("开下一局失败: %v", err)
			}

		case game.PhaseGameEnd:
			// 由 StartNextRound 进入；循环条件会退出。

		default:
			res.Aborted = true
			res.Error = fmt.Sprintf("未知阶段 %v", state.Phase)
		}
		if res.Error != "" {
			break
		}
	}

	res.Actions = actions
	res.FinalScores = state.Scores
	res.Ranks = ranksOf(state.Scores)
	res.DurationMs = float64(time.Since(started).Microseconds()) / 1000
	return res
}

// decideOne 让 agent 对一次观察做决策，并记录耗时与决策次数。
func decideOne(a agent.Agent, obs game.Observation, seat game.Seat, timeout time.Duration, res *GameResult) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		// 诊断信息：出问题时能直接看出是哪种非法状态（手牌/副露/立直/摸牌）。
		return game.Action{}, fmt.Errorf(
			"没有合法动作（seat=%d hand=%d melds=%d riichi=%v drawn=%v wall=%d）",
			seat, len(obs.Hand), len(obs.Melds[seat]), obs.Riichi[seat], obs.Drawn != nil, obs.WallRemaining)
	}
	started := time.Now()
	action, err := agent.DecideWithContext(context.Background(), a, obs, timeout)
	elapsed := time.Since(started)
	res.Seats[seat].Decisions++
	res.LatenciesMs[seat] = append(res.LatenciesMs[seat], float64(elapsed.Microseconds())/1000)
	if err != nil {
		return game.Action{}, err
	}
	return action, nil
}

// forceWin 在合法动作里存在自摸时强制选择和了。
//
// S8 的内置策略（shanten/defense/search）目前只输出 ActDiscard，不会主动叫和；
// 评测层把「能胡必胡」作为统一规则补上，否则和了率会被策略的实现细节掩盖。
func forceWin(obs game.Observation, action game.Action) game.Action {
	if action.Kind == game.ActTsumo {
		return action
	}
	for _, legal := range obs.LegalActions {
		if legal.Kind == game.ActTsumo {
			return legal
		}
	}
	return action
}

// decideResponse 处理响应窗口里的一个座位。
func decideResponse(
	a agent.Agent,
	state *game.State,
	seat game.Seat,
	responses []game.Action,
	cfg Config,
	res *GameResult,
) (game.Action, error) {
	// pass 模式：只接受荣和，其余一律 pass（保证全门清，计分精确）。
	if cfg.Calls == CallsPass {
		for _, option := range responses {
			if option.Kind == game.ActRon {
				return option, nil
			}
		}
		return game.Action{Kind: game.ActPass, Seat: seat}, nil
	}
	return decideOne(a, state.Observe(seat), seat, cfg.DecisionTimeout, res)
}
