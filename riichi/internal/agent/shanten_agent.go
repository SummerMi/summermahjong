package agent

import (
	"context"

	"summermahjong/riichi/internal/game"
)

// ShantenAgent 是「向听效率」策略：
//
//  1. 对每张合法切牌计算切出后的向听数（rules.Shanten）；
//  2. 只在向听数最小的候选里计算有效进张（ukeire，按剩余张数加权）；
//  3. 用 Style 权重把向听 / 进张 / 牌价值 / 危险度折成一个分数，取最小。
//
// 进张只对「最小向听」的候选计算，是为了把单步控制在毫秒内：
// 有效进张需要对 34 种牌各做一次 14 张形态的 DFS，只在少数最优候选上做。
type ShantenAgent struct {
	style Style
}

// NewShantenAgent 构造向听策略；style 的零值按 StyleAttack 处理。
func NewShantenAgent(style Style) *ShantenAgent {
	return &ShantenAgent{style: normalizeStyle(style, StyleAttack)}
}

// Name 实现 Agent。
func (a *ShantenAgent) Name() string { return "shanten:" + a.style.Name }

// Decide 实现 Agent：返回向听优先、进张次之的最优切牌。
func (a *ShantenAgent) Decide(ctx context.Context, obs game.Observation) (game.Action, error) {
	if err := ctx.Err(); err != nil {
		return game.Action{}, err
	}
	if len(obs.Hand) == 0 || len(candidateDiscards(obs)) == 0 {
		return fallbackAction(obs)
	}

	meldCount := ownMeldCount(obs)
	visible := visibleCounts(obs)
	evals := evaluateCandidates(obs, a.style, meldCount, visible)
	if len(evals) == 0 {
		return fallbackAction(obs)
	}

	// 有效进张只补在最小向听的候选上（其余候选靠向听惩罚自然淘汰）。
	minS := minShanten(evals)
	for i := range evals {
		if evals[i].Shanten == minS {
			withUkeire(obs, &evals[i], a.style, meldCount, visible)
		}
	}

	best, ok := bestByScore(evals)
	if !ok {
		return fallbackAction(obs)
	}
	return maybeRiichi(obs, best.Tile), nil
}

// normalizeStyle 把零值 Style 替换成默认打风，避免调用方漏传时权重全为 0。
func normalizeStyle(style, fallback Style) Style {
	if style.Name == "" {
		return fallback
	}
	return style
}
