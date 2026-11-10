// Package agent 定义智能体接口与内置策略。
//
// 契约（docs/riichi-接口契约-20261110.md §4）：
//   - Agent 只能读 game.Observation，不得直接访问 game.State；
//   - Decide 返回的 Action 必须在 obs.LegalActions 内；
//   - 默认决策上限 500ms，超时由调用方回退到启发式首选。
package agent

import (
	"context"
	"errors"
	"math/rand"

	"summermahjong/riichi/internal/game"
)

// ErrNoLegalAction 表示当前没有可执行动作（对局已结束或状态异常）。
var ErrNoLegalAction = errors.New("agent: no legal action")

// Agent 是一个麻将智能体。
type Agent interface {
	// Name 返回策略名，用于回放与评测报告。
	Name() string
	// Decide 在 obs.LegalActions 中选一个动作。
	Decide(ctx context.Context, obs game.Observation) (game.Action, error)
}

// Random 是随机策略：在所有合法动作里等概率选一个。
//
// 它是 Agent 接口的参考实现，也用于「对局链路是否打通」的冒烟测试。
type Random struct {
	rng *rand.Rand
}

// NewRandom 构造随机策略；seed 固定则决策序列可复现。
func NewRandom(seed int64) *Random {
	return &Random{rng: rand.New(rand.NewSource(seed))}
}

// Name 实现 Agent。
func (r *Random) Name() string { return "random" }

// Decide 实现 Agent：等概率选一个合法动作。
func (r *Random) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		return game.Action{}, ErrNoLegalAction
	}
	return obs.LegalActions[r.rng.Intn(len(obs.LegalActions))], nil
}

// FirstLegal 是兜底策略：总选第一个合法动作（超时/异常时使用）。
type FirstLegal struct{}

// Name 实现 Agent。
func (FirstLegal) Name() string { return "first_legal" }

// Decide 实现 Agent。
func (FirstLegal) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		return game.Action{}, ErrNoLegalAction
	}
	return obs.LegalActions[0], nil
}

// InLegalActions 报告 a 是否是 obs.LegalActions 中的合法动作。
//
// 服务端在落子前必须调用它：Agent 返回越权动作时丢弃并回退。
func InLegalActions(obs game.Observation, a game.Action) bool {
	for _, legal := range obs.LegalActions {
		if legal.Kind != a.Kind {
			continue
		}
		// 摸牌不区分牌面；打牌要求牌面一致。
		if a.Kind != game.ActDiscard || legal.Tile == a.Tile {
			return true
		}
	}
	return false
}
