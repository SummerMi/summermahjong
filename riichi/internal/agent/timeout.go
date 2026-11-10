package agent

import (
	"context"
	"errors"
	"time"

	"summermahjong/riichi/internal/game"
)

// errAgentPanic 是 Agent 内部 panic 的内部标记（对调用方统一表现为回退）。
var errAgentPanic = errors.New("agent: decision panicked")

// DecideWithTimeout 在 timeout 内运行 a.Decide，超时/出错/返回非法动作时
// 回退到启发式首选（FirstLegal），并保证返回的动作一定在 obs.LegalActions 内。
//
// 契约 §4：默认决策上限 500ms，超时回退首选。这里把「回退」做成对调用方透明：
// 只要 obs 里还有合法动作，就返回一个合法动作且 error == nil；
// 只有 obs.LegalActions 为空时才返回 ErrNoLegalAction。
//
// 注意：实现里有 goroutine 兜底；如果 a.Decide 完全不响应 ctx，超时后该 goroutine
// 会自行跑完（结果通道带缓冲，不会阻塞），服务端不应依赖它立即退出。
// 内置策略的 Decide 都会在入口检查 ctx.Err()，正常不会拖到超时。
func DecideWithTimeout(a Agent, obs game.Observation, timeout time.Duration) (game.Action, error) {
	return DecideWithContext(context.Background(), a, obs, timeout)
}

// DecideWithContext 与 DecideWithTimeout 相同，但允许调用方传入父 context
// （HTTP 请求取消、房间关闭等场景可提前放弃）。
func DecideWithContext(ctx context.Context, a Agent, obs game.Observation, timeout time.Duration) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		return game.Action{}, ErrNoLegalAction
	}
	fallback := func() (game.Action, error) { return fallbackAction(obs) }
	if a == nil || timeout <= 0 {
		return fallback()
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		action game.Action
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		// 内置策略不 panic；这里兜底，保证第三方 Agent 出错时服务不会崩。
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: errAgentPanic}
			}
		}()
		action, err := a.Decide(runCtx, obs)
		done <- outcome{action: action, err: err}
	}()

	select {
	case <-runCtx.Done():
		// 超时或父 context 取消：回退到启发式首选。
		return fallback()
	case result := <-done:
		if result.err != nil {
			return fallback()
		}
		// 防御：Agent 返回越权动作时丢弃并回退（契约 §4）。
		if !InLegalActions(obs, result.action) {
			return fallback()
		}
		return result.action, nil
	}
}
