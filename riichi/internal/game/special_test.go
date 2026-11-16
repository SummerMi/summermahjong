package game

import (
	"encoding/json"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// ── 特殊和了/途中流局（G1 天和/地和、G2 三家和、G5 四家立直）────────────

// ryuukyokuPayload 返回最后一条 ryuukyoku 事件的 payload（测试用）。
func ryuukyokuPayload(t *testing.T, s *State) map[string]any {
	t.Helper()
	for i := len(s.Events) - 1; i >= 0; i-- {
		if s.Events[i].Kind != EventRyuukyoku {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(s.Events[i].Payload, &payload); err != nil {
			t.Fatalf("解析 ryuukyoku payload: %v", err)
		}
		return payload
	}
	t.Fatal("没有 ryuukyoku 事件")
	return nil
}

// tsumoPayload 返回最后一条 tsumo 事件的 payload。
func tsumoPayload(t *testing.T, s *State) map[string]any {
	t.Helper()
	for i := len(s.Events) - 1; i >= 0; i-- {
		if s.Events[i].Kind != EventTsumo {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(s.Events[i].Payload, &payload); err != nil {
			t.Fatalf("解析 tsumo payload: %v", err)
		}
		return payload
	}
	t.Fatal("没有 tsumo 事件")
	return nil
}

// hasYaku 报告 payload 的 yaku 列表里是否包含 name。
func hasYaku(payload map[string]any, name string) bool {
	list, ok := payload["yaku"].([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if text, ok := item.(string); ok && text == name {
			return true
		}
	}
	return false
}

// TestSanchaAbortiveDraw 三家和了：三家同时荣和 → 途中流局，不结算、不产生 ron 事件。
func TestSanchaAbortiveDraw(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p", // seat0 打 5p
		"123m456m789m11p34p",           // seat1 可荣和 5p
		"123m456m789m11p34p",           // seat2 可荣和 5p
		"123m456m789m11p34p",           // seat3 可荣和 5p
	)
	applyDiscard(t, s, 0, "5p")

	if !optionKinds(s, 1)[ActRon] || !optionKinds(s, 2)[ActRon] || !optionKinds(s, 3)[ActRon] {
		t.Fatal("三家都应有荣和选项")
	}
	applyRon(t, s, 1, "5p")
	applyRon(t, s, 2, "5p")
	applyRon(t, s, 3, "5p")

	if s.Phase != PhaseRoundEnd {
		t.Fatalf("三家和后 phase=%v want round_end", s.Phase)
	}
	if n := countEvents(s, EventRon); n != 0 {
		t.Fatalf("三家和不应结算荣和，ron 事件数=%d", n)
	}
	for seat, score := range s.Scores {
		if score != 25000 {
			t.Fatalf("三家和不改变点数，seat%d=%d want 25000", seat, score)
		}
	}
	payload := ryuukyokuPayload(t, s)
	if payload["reason"] != RyuukyokuSancha {
		t.Fatalf("reason=%v want %s", payload["reason"], RyuukyokuSancha)
	}
	if deltas, ok := payload["score_deltas"].([]any); ok {
		for i, delta := range deltas {
			if delta.(float64) != 0 {
				t.Fatalf("三家和 score_deltas[%d]=%v want 0", i, delta)
			}
		}
	}

	// 途中流局：亲家连庄、本场 +1
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	if s.Honba != 1 || s.Dealer != 0 {
		t.Fatalf("三家和后应亲家连庄本场+1，honba=%d dealer=%d", s.Honba, s.Dealer)
	}
}

// TestTenhouDealerFirstDraw 天和：亲家第一次摸牌即自摸，记 13 翻役满。
func TestTenhouDealerFirstDraw(t *testing.T) {
	s := scenario(t,
		"123m456m789m11p123s", // 14 张，最后一张（3s）为刚摸到的牌
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Apply(Action{Kind: ActTsumo, Seat: 0})

	if s.Phase != PhaseRoundEnd {
		t.Fatalf("天和 phase=%v want round_end", s.Phase)
	}
	payload := tsumoPayload(t, s)
	if !hasYaku(payload, "天和") {
		t.Fatalf("应记天和役满，yaku=%v", payload["yaku"])
	}
	// 亲家役满自摸：每家 16000，合计 48000
	if s.Scores[0] != 73000 || s.Scores[1] != 9000 || s.Scores[2] != 9000 || s.Scores[3] != 9000 {
		t.Fatalf("天和点数=%v，期望 73000/9000/9000/9000", s.Scores)
	}
}

// TestChiihouNonDealerFirstDraw 地和：子家第一次摸牌即自摸，记 13 翻役满。
func TestChiihouNonDealerFirstDraw(t *testing.T) {
	s := mustManualState(t, 20261110)
	s.Hands[0] = mustHand(t, "1m4m7m1s4s7s2p3p6p8p1z2z3z")
	s.Hands[1] = mustHand(t, "123m456m789m11p123s") // 14 张，最后一张为刚摸到的牌
	s.Hands[2] = mustHand(t, "1m4m7m1s4s7s2p3p6p8p1z2z3z")
	s.Hands[3] = mustHand(t, "1m4m7m1s4s7s2p3p6p8p1z2z3z")
	s.Turn = 1
	s.Phase = PhaseAwaitDiscard
	s.DrawCount = 2
	s.SeatDraws = [4]int{1, 1, 0, 0}
	drawn := s.Hands[1][len(s.Hands[1])-1]
	s.Drawn = &drawn

	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("地和自摸失败: %v", err)
	}
	payload := tsumoPayload(t, s)
	if !hasYaku(payload, "地和") {
		t.Fatalf("应记地和役满，yaku=%v", payload["yaku"])
	}
	// 子家役满自摸：亲家 16000、两个子家各 8000，合计 32000
	if s.Scores[1] != 57000 || s.Scores[0] != 9000 || s.Scores[2] != 17000 || s.Scores[3] != 17000 {
		t.Fatalf("地和点数=%v，期望 57000/9000/17000/17000", s.Scores)
	}
}

// TestSuuchaRiichiAbortiveDraw 四家立直：第 4 家宣言牌打出后途中流局。
func TestSuuchaRiichiAbortiveDraw(t *testing.T) {
	s := scenario(t,
		"123m456m789m11p34p5p", // 打 5p 后听 2p/5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Riichi = [4]bool{false, true, true, true}
	s.RiichiStix = 3

	if err := s.Apply(Action{Kind: ActRiichi, Seat: 0, Tile: mustTile(t, "5p")}); err != nil {
		t.Fatalf("第 4 家立直失败: %v", err)
	}

	if s.Phase != PhaseRoundEnd {
		t.Fatalf("四家立直后 phase=%v want round_end", s.Phase)
	}
	payload := ryuukyokuPayload(t, s)
	if payload["reason"] != RyuukyokuSuucha {
		t.Fatalf("reason=%v want %s", payload["reason"], RyuukyokuSuucha)
	}
	if n := countEvents(s, EventRon); n != 0 {
		t.Fatalf("四家立直途中流局不应有 ron 事件，实际 %d", n)
	}
	if s.RiichiStix != 4 {
		t.Fatalf("立直棒应留在场上（4 根），实际 %d", s.RiichiStix)
	}
	if !s.LastDealerRepeat {
		t.Fatal("四家立直是途中流局，应亲家连庄")
	}
}

// ── 流し満貫（G4）─────────────────────────────────────────────

// TestNagashiManganDealer 亲家流し満貫：满贯自摸（每家 4000），亲家连庄。
func TestNagashiManganDealer(t *testing.T) {
	s := mustState(t, 42)
	s.Discards[0] = mustHand(t, "1m 9m 1p 9p 1s 9s 1z 2z 3z 4z 5z 6z 7z")
	s.Discards[1] = mustHand(t, "2m 3m 4m")
	s.Discards[2] = mustHand(t, "2p 3p 4p")
	s.Discards[3] = mustHand(t, "2s 3s 4s")
	s.finishRyuukyoku(RyuukyokuHaitei, false)

	payload := ryuukyokuPayload(t, s)
	if payload["reason"] != RyuukyokuNagashi {
		t.Fatalf("reason=%v want %s", payload["reason"], RyuukyokuNagashi)
	}
	if s.Scores[0] != 37000 || s.Scores[1] != 21000 || s.Scores[2] != 21000 || s.Scores[3] != 21000 {
		t.Fatalf("亲家流し満貫点数=%v，期望 37000/21000/21000/21000", s.Scores)
	}
	if !s.LastDealerRepeat {
		t.Fatal("亲家流し満貫应连庄")
	}
}

// TestNagashiManganNonDealerRotates 子家流し満貫：亲家付 4000、其他子家各 2000，亲家轮转。
func TestNagashiManganNonDealerRotates(t *testing.T) {
	s := mustState(t, 42)
	s.Discards[0] = mustHand(t, "2m 3m 4m")
	s.Discards[1] = mustHand(t, "1m 9m 1p 9p 1s 9s 1z 2z 3z 4z 5z 6z 7z")
	s.Discards[2] = mustHand(t, "2p 3p 4p")
	s.Discards[3] = mustHand(t, "2s 3s 4s")
	s.finishRyuukyoku(RyuukyokuHaitei, false)

	if s.Scores[1] != 33000 || s.Scores[0] != 21000 || s.Scores[2] != 23000 || s.Scores[3] != 23000 {
		t.Fatalf("子家流し満貫点数=%v，期望 21000/33000/23000/23000", s.Scores)
	}
	if s.LastDealerRepeat {
		t.Fatal("子家流し満貫不应连庄")
	}
}

// TestNagashiBlockedByCalledDiscard 舍牌被鸣走时不成立流し満貫。
func TestNagashiBlockedByCalledDiscard(t *testing.T) {
	s := mustState(t, 42)
	s.Discards[0] = mustHand(t, "2m 3m 4m")
	s.Discards[1] = mustHand(t, "1m 9m 1p 9p 1s 9s 1z 2z 3z 4z 5z 6z 7z")
	s.Discards[2] = mustHand(t, "2p 3p 4p")
	s.Discards[3] = mustHand(t, "2s 3s 4s")
	s.Melds[2] = []Meld{{Kind: MeldPon, From: 1, Tiles: []tile.Tile{{Kind: tile.Man1}, {Kind: tile.Man1}, {Kind: tile.Man1}}}}
	// 被鸣走的牌已从牌河移除；这里只看 meld.From 是否命中。
	s.finishRyuukyoku(RyuukyokuHaitei, false)

	payload := ryuukyokuPayload(t, s)
	if payload["reason"] != RyuukyokuHaitei {
		t.Fatalf("舍牌被鸣时不应流し満貫，reason=%v", payload["reason"])
	}
	if _, ok := payload["nagashi_seats"]; ok {
		t.Fatal("舍牌被鸣时不应有 nagashi_seats")
	}
}
