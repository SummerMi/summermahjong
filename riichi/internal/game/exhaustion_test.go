package game

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestWallExhaustionEmitsRoundEndAndCanStartNext 活牌摸完的完整语义：
//
//   - 第 71 次摸牌（牌山已空）返回 error（驱动方据此停止本局循环）；
//   - 但 ryuukyoku / round_end 事件必须已经写入 State.Events（transport room 依赖
//     「返回错误也要先把新增事件落库/广播」，否则会丢事件卡在 await_draw —— S11 现场 bug）；
//   - 之后可以正常 StartNextRound 开下一局。
func TestWallExhaustionEmitsRoundEndAndCanStartNext(t *testing.T) {
	s := mustNew(t, 5)
	draws := 0
	for {
		if s.Phase == PhaseAwaitDraw {
			err := s.Apply(Action{Kind: ActDraw, Seat: s.Turn})
			if err != nil {
				// 摸空：合法终局，返回 error 但事件已写入
				if s.Phase != PhaseRoundEnd {
					t.Fatalf("摸空返回 error 但 phase=%v，期望 RoundEnd", s.Phase)
				}
				break
			}
			draws++
			continue
		}
		if err := s.Apply(Action{Kind: ActDiscard, Seat: s.Turn, Tile: s.Hands[s.Turn][0]}); err != nil {
			t.Fatalf("discard: %v", err)
		}
	}
	if draws != tile.MaxDrawsFromLive {
		t.Fatalf("draws=%d want %d", draws, tile.MaxDrawsFromLive)
	}
	if !hasEventKind(s, EventRyuukyoku) || !hasEventKind(s, EventRoundEnd) {
		t.Fatalf("摸空后应已有 ryuukyoku + round_end 事件，实际=%v", eventKinds(s))
	}
	if s.Wall.Remaining() != 0 {
		t.Fatalf("活牌剩余=%d want 0", s.Wall.Remaining())
	}

	// 能开下一局
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("荒牌流局后 StartNextRound: %v", err)
	}
	if s.Phase != PhaseAwaitDraw {
		t.Fatalf("下一局 phase=%v want AwaitDraw", s.Phase)
	}
	if len(s.Events) == 0 || s.Events[len(s.Events)-1].Kind != EventRoundStart {
		t.Fatal("下一局应追加 round_start 事件")
	}
}
