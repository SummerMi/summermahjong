package agent

import (
	"context"
	"math"

	"summermahjong/riichi/internal/game"
)

// searchBeam 是搜索策略的候选宽度：只对 1 层估值最好的前几张切牌做深度展开。
const searchBeam = 3

// SearchAgent 是「限深枚举」策略：不跑蒙特卡洛，只做确定性的两层展开。
//
//   - 第 1 层（切牌）：对每张合法切牌算切后向听、有效进张、危险度；
//   - 第 2 层（进张）：对前 searchBeam 张候选，枚举 34 种可能摸到的牌，
//     用 shantenAny（14 张形态，等价于「摸到后打出最优一张」）得到下一巡向听，
//     按剩余张数加权求期望；
//   - 用期望向听替换第 1 层的即时向听，再叠加 Style 的进张/危险度/牌价值权重。
//
// 枚举宽度固定为 3×34 次 DFS，单步耗时在毫秒级（有 P95 测试守着 <5ms），
// 比全量蒙特卡洛便宜几个数量级，同时比纯 1 层的向听策略多看了「下一巡」。
type SearchAgent struct {
	style Style
}

// NewSearchAgent 构造搜索策略；style 的零值按 StyleClosed 处理。
func NewSearchAgent(style Style) *SearchAgent {
	return &SearchAgent{style: normalizeStyle(style, StyleClosed)}
}

// Name 实现 Agent。
func (a *SearchAgent) Name() string { return "search:" + a.style.Name }

// Decide 实现 Agent。
func (a *SearchAgent) Decide(ctx context.Context, obs game.Observation) (game.Action, error) {
	if err := ctx.Err(); err != nil {
		return game.Action{}, err
	}
	if len(obs.Responses) > 0 {
		return decideResponseAction(obs, a.style), nil
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

	// 先按「向听优先、安全次之」取 beam；再对 beam 内候选做进张与下一巡展开。
	sortByShantenThenDanger(evals)
	beam := searchBeam
	if len(evals) < beam {
		beam = len(evals)
	}

	best := scoredDiscard{}
	bestScore := math.Inf(1)
	found := false
	for i := 0; i < beam; i++ {
		ev := evals[i]
		after, ok := removeTile(obs.Hand, ev.Tile)
		if !ok {
			continue
		}
		// 一次遍历同时得到有效进张与「摸一张再打出最优」的期望向听。
		uk, expected := analyzeAfter(after, meldCount, ev.Shanten, visible)
		ev.Ukeire = uk
		ev.Score = expected*a.style.ShantenWeight -
			float64(ev.Ukeire)*a.style.UkeireWeight -
			(maxKeepValue-ev.KeepValue)*a.style.KeepValueWeight +
			ev.Danger*a.style.DangerWeight
		if !found || ev.Score < bestScore {
			best = ev
			bestScore = ev.Score
			found = true
		}
	}
	if !found {
		return fallbackAction(obs)
	}
	return maybeRiichi(obs, best.Tile), nil
}
