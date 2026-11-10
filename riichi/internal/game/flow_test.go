package game

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// ── 测试辅助 ────────────────────────────────────────────────────

// mustHand 解析手牌文本；失败直接 Fatal。
func mustHand(t *testing.T, text string) []tile.Tile {
	t.Helper()
	hand, err := tile.ParseHand(text)
	if err != nil {
		t.Fatalf("解析手牌 %q 失败: %v", text, err)
	}
	return hand
}

// mustTile 解析单张牌文本。
func mustTile(t *testing.T, text string) tile.Tile {
	t.Helper()
	tl, err := tile.ParseTile(text)
	if err != nil {
		t.Fatalf("解析牌 %q 失败: %v", text, err)
	}
	return tl
}

// mustState 构造默认规则的状态（自动 pass 响应）。
func mustState(t *testing.T, seed int64) *State {
	t.Helper()
	s, err := New(seed, DefaultRuleset())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// mustManualState 构造手动响应模式的状态。
func mustManualState(t *testing.T, seed int64) *State {
	t.Helper()
	s, err := NewWithOptions(seed, DefaultRuleset(), Options{ManualResponses: true})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return s
}

// scenario 覆盖四家手牌，构造「庄家已摸牌、等待打牌」的可控局面。
// seat0Hand 必须是 14 张（最后一张视为刚摸到），其余三家各 13 张。
func scenario(t *testing.T, seat0Hand, h1, h2, h3 string) *State {
	t.Helper()
	s := mustManualState(t, 20261110)
	texts := [4]string{seat0Hand, h1, h2, h3}
	for seat := 0; seat < 4; seat++ {
		s.Hands[seat] = mustHand(t, texts[seat])
		s.Melds[seat] = nil
		s.Discards[seat] = nil
	}
	if len(s.Hands[0]) != 14 {
		t.Fatalf("seat0 手牌应为 14 张，实际 %d", len(s.Hands[0]))
	}
	for seat := 1; seat < 4; seat++ {
		if len(s.Hands[seat]) != 13 {
			t.Fatalf("seat%d 手牌应为 13 张，实际 %d", seat, len(s.Hands[seat]))
		}
	}
	drawn := s.Hands[0][13]
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Turn = 0
	s.DrawCount = 1
	s.SeatDraws = [4]int{1, 0, 0, 0}
	return s
}

// scenarioWithMelds 覆盖四家手牌并给 seat0 预置副露；seat0 手牌须为 14-3*len(melds) 张。
func scenarioWithMelds(t *testing.T, seat0Hand string, melds0 []Meld, h1, h2, h3 string) *State {
	t.Helper()
	s := mustManualState(t, 20261110)
	s.Hands[0] = mustHand(t, seat0Hand)
	s.Melds[0] = melds0
	s.Discards[0] = nil
	others := [3]string{h1, h2, h3}
	for i := 1; i < 4; i++ {
		s.Hands[i] = mustHand(t, others[i-1])
		s.Melds[i] = nil
		s.Discards[i] = nil
		if len(s.Hands[i]) != 13 {
			t.Fatalf("seat%d 手牌应为 13 张，实际 %d", i, len(s.Hands[i]))
		}
	}
	want := 14 - 3*len(melds0)
	if len(s.Hands[0]) != want {
		t.Fatalf("seat0 手牌应为 %d 张，实际 %d", want, len(s.Hands[0]))
	}
	drawn := s.Hands[0][len(s.Hands[0])-1]
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Turn = 0
	s.DrawCount = 1
	s.SeatDraws = [4]int{1, 0, 0, 0}
	return s
}

// applyDiscard 让座位打出指定牌；失败 Fatal。
func applyDiscard(t *testing.T, s *State, seat Seat, text string) {
	t.Helper()
	if err := s.Apply(Action{Kind: ActDiscard, Seat: seat, Tile: mustTile(t, text)}); err != nil {
		t.Fatalf("seat%d 打 %s 失败: %v", seat, text, err)
	}
}

// applyResponse 提交响应动作；失败 Fatal。
func applyResponse(t *testing.T, s *State, seat Seat, kind ActionKind) {
	t.Helper()
	action := Action{Kind: kind, Seat: seat}
	if err := s.Apply(action); err != nil {
		t.Fatalf("seat%d 响应 %v 失败: %v", seat, kind, err)
	}
}

// applyRon 提交荣和响应。
func applyRon(t *testing.T, s *State, seat Seat, text string) {
	t.Helper()
	if err := s.Apply(Action{Kind: ActRon, Seat: seat, Tile: mustTile(t, text)}); err != nil {
		t.Fatalf("seat%d 荣和失败: %v", seat, err)
	}
}

// optionKinds 提取某座位的响应动作类型集合。
func optionKinds(s *State, seat Seat) map[ActionKind]bool {
	out := map[ActionKind]bool{}
	if s.Pending == nil {
		return out
	}
	for _, action := range s.Pending.Options[seat] {
		out[action.Kind] = true
	}
	return out
}

// eventKinds 返回事件类型序列（便于断言）。
func eventKinds(s *State) []EventKind {
	out := make([]EventKind, 0, len(s.Events))
	for _, ev := range s.Events {
		out = append(out, ev.Kind)
	}
	return out
}

// lastEvent 返回最后一条事件。
func lastEvent(t *testing.T, s *State) Event {
	t.Helper()
	if len(s.Events) == 0 {
		t.Fatal("没有事件")
	}
	return s.Events[len(s.Events)-1]
}

// hasEventKind 报告是否出现过某类事件。
func hasEventKind(s *State, kind EventKind) bool {
	for _, ev := range s.Events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

// countEvents 统计某类事件出现次数。
func countEvents(s *State, kind EventKind) int {
	n := 0
	for _, ev := range s.Events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// ── 响应窗口：优先级 ────────────────────────────────────────────

// TestResponseRonBeatsPon 断言荣和优先于碰：即使有人选择碰，荣和成立即结算。
func TestResponseRonBeatsPon(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p", // seat0 打 5p
		"123m456m789m11p34p",           // seat1 荣和 5p
		"5p5p123m456m789m1p2p",         // seat2 可碰
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",   // seat3 无响应
	)
	applyDiscard(t, s, 0, "5p")

	if s.Phase != PhaseAwaitResponse {
		t.Fatalf("phase=%v want await_response", s.Phase)
	}
	if !optionKinds(s, 1)[ActRon] {
		t.Fatal("seat1 应有荣和选项")
	}
	if !optionKinds(s, 2)[ActPon] {
		t.Fatal("seat2 应有碰选项")
	}
	if len(s.Pending.pendingSeats()) != 2 {
		t.Fatalf("待响应座位数=%d want 2", len(s.Pending.pendingSeats()))
	}

	applyRon(t, s, 1, "5p")
	applyResponse(t, s, 2, ActPon) // 选择碰也应被荣和压过

	if s.Phase != PhaseRoundEnd {
		t.Fatalf("荣和后 phase=%v want round_end", s.Phase)
	}
	if countEvents(s, EventRon) != 1 {
		t.Fatalf("ron 事件数=%d want 1", countEvents(s, EventRon))
	}
	if s.Melds[2] != nil && len(s.Melds[2]) != 0 {
		t.Fatal("荣和优先时碰不应成立")
	}
	// 庄家（seat0）放铳给闲家（seat1）：闲家荣和 1000 点
	// 123m456m789m 是一气通贯（2 翻）+ 平和（1 翻），荣和 3900
	if s.Scores[1] != 28900 || s.Scores[0] != 21100 {
		t.Fatalf("scores=%v，期望 seat1=28900 seat0=21100", s.Scores)
	}
}

// TestResponseDoubleRon 双响：两家同时荣和都成立。
func TestResponseDoubleRon(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 荣和 5p
		"123m456m789m11p34p", // seat2 也荣和 5p（不同花色牌型相同）
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Rules.DoubleRon = true
	applyDiscard(t, s, 0, "5p")
	applyRon(t, s, 1, "5p")
	applyRon(t, s, 2, "5p")

	if countEvents(s, EventRon) != 2 {
		t.Fatalf("双响应产生 2 条 ron 事件，实际 %d", countEvents(s, EventRon))
	}
	if s.Scores[1] != 28900 || s.Scores[2] != 28900 || s.Scores[0] != 17200 {
		t.Fatalf("双响点数=%v，期望 seat0=17200 seat1=28900 seat2=28900", s.Scores)
	}
}

// TestResponseDoubleRonDisabled 头跳：关闭双响时只有最近一家（seat1）成立。
func TestResponseDoubleRonDisabled(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p",
		"123m456m789m11p34p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Rules.DoubleRon = false
	applyDiscard(t, s, 0, "5p")
	applyRon(t, s, 1, "5p")
	applyRon(t, s, 2, "5p")

	if countEvents(s, EventRon) != 1 {
		t.Fatalf("头跳应只有 1 条 ron 事件，实际 %d", countEvents(s, EventRon))
	}
	if s.Scores[1] != 28900 || s.Scores[2] != 25000 {
		t.Fatalf("头跳点数=%v，期望 seat1=28900 seat2=25000", s.Scores)
	}
}

// TestResponsePonBeatsChi 碰优先于吃（即使吃的是下家）。
func TestResponsePonBeatsChi(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p",  // seat1 可吃 5p → 345p
		"5p5p123m456m789m1p2p", // seat2 可碰
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	if !optionKinds(s, 1)[ActChi] {
		t.Fatal("seat1 应可吃")
	}
	if !optionKinds(s, 2)[ActPon] {
		t.Fatal("seat2 应可碰")
	}

	// seat1 选择吃，seat2 选择碰 → 碰压过吃
	if err := s.Apply(Action{Kind: ActChi, Seat: 1, Tile: mustTile(t, "5p"),
		Consumed: s.Pending.Options[1][len(s.Pending.Options[1])-1].Consumed}); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	applyResponse(t, s, 2, ActPon)

	if countEvents(s, EventPon) != 1 || countEvents(s, EventChi) != 0 {
		t.Fatalf("事件=%v，期望 pon 成立、chi 不成立", eventKinds(s))
	}
	if s.Turn != 2 || s.Phase != PhaseAwaitDiscard {
		t.Fatalf("碰后 turn=%d phase=%v，期望 seat2/await_discard", s.Turn, s.Phase)
	}
	if len(s.Melds[2]) != 1 || s.Melds[2][0].Kind != MeldPon {
		t.Fatalf("seat2 副露=%v", s.Melds[2])
	}
}

// TestResponseChiOnlyFromLeft 吃只能吃上家：非下家即使有牌也没有吃选项。
func TestResponseChiOnlyFromLeft(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"3p4p123m456m789m11p", // seat3 有 3p4p，但不是 seat0 的下家
	)
	applyDiscard(t, s, 0, "5p")
	if optionKinds(s, 3)[ActChi] {
		t.Fatal("seat3 不是上家下家，不应有吃选项")
	}
	// 只有 seat1 可能吃；若它没有 3p4p 则整个窗口无人可响应 → 直接推进
}

// TestResponseChiSucceeds 只有一家吃时，吃成立并轮转到吃的人。
func TestResponseChiSucceeds(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	options := s.Pending.Options[1]
	var chiAction Action
	for _, action := range options {
		if action.Kind == ActChi {
			chiAction = action
		}
	}
	if chiAction.Kind != ActChi {
		t.Fatal("seat1 应有吃选项")
	}
	if err := s.Apply(chiAction); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	if countEvents(s, EventChi) != 1 {
		t.Fatalf("chi 事件数=%d want 1", countEvents(s, EventChi))
	}
	if s.Turn != 1 || s.Phase != PhaseAwaitDiscard {
		t.Fatalf("吃的轮转=%d/%v，期望 seat1/await_discard", s.Turn, s.Phase)
	}
	if len(s.Melds[1]) != 1 || s.Melds[1][0].Kind != MeldChi {
		t.Fatalf("seat1 副露=%v", s.Melds[1])
	}
	// 被吃的牌从牌河移除
	if len(s.Discards[0]) != 0 {
		t.Fatalf("牌河=%v，被吃的牌应移除", s.Discards[0])
	}
	// 回归：吃/碰后没有「摸到的牌」，但仍必须能正常打牌（S5 曾因 Drawn==nil 卡死）
	legal := s.Legal()
	if len(legal) == 0 {
		t.Fatal("吃后 Legal 不应为空")
	}
	discard := legal[0]
	if discard.Kind != ActDiscard {
		t.Fatalf("吃后首个合法动作=%v want discard", discard.Kind)
	}
	if err := s.Apply(discard); err != nil {
		t.Fatalf("吃后打牌失败: %v", err)
	}
	if s.Phase != PhaseAwaitDraw && s.Phase != PhaseAwaitResponse {
		t.Fatalf("吃后打牌 phase=%v", s.Phase)
	}
}

// TestResponseTimeoutAllPass 响应超时：全部视为 pass，轮到下家摸牌。
func TestResponseTimeoutAllPass(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 可荣和
		"5p5p123m456m789m1p2p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	if err := s.ResolveTimeout(); err != nil {
		t.Fatalf("ResolveTimeout: %v", err)
	}
	if s.Phase != PhaseAwaitDraw || s.Turn != 1 {
		t.Fatalf("超时后 turn=%d phase=%v，期望 seat1/await_draw", s.Turn, s.Phase)
	}
	if countEvents(s, EventRon) != 0 {
		t.Fatal("超时不应产生荣和")
	}
	// 放弃荣和 → 同巡振听
	if !s.TempFuriten[1] {
		t.Fatal("放弃荣和后应进入同巡振听")
	}
}

// TestResponseRonBeatsDaiminkan 荣和优先于大明杠。
func TestResponseRonBeatsDaiminkan(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p",     // seat1 荣和
		"5p5p5p123m456m1p2p3p9p", // seat2 可大明杠（3 张 5p）
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	if !optionKinds(s, 2)[ActKanDaiminkan] {
		t.Fatal("seat2 应有大明杠选项")
	}
	applyRon(t, s, 1, "5p")
	applyResponse(t, s, 2, ActKanDaiminkan)
	if countEvents(s, EventKan) != 0 {
		t.Fatalf("荣和优先，杠不应成立: %v", eventKinds(s))
	}
	if countEvents(s, EventRon) != 1 {
		t.Fatal("应有荣和事件")
	}
}

// TestResponsePassThenRonByOther 一家 pass 不影响另一家荣和。
func TestResponsePassThenRonByOther(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"123m456m789m11p34p", // seat2 荣和
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	// seat1 无选项自动 pass；seat2 荣和
	applyRon(t, s, 2, "5p")
	if countEvents(s, EventRon) != 1 || s.Scores[2] != 28900 {
		t.Fatalf("scores=%v events=%v", s.Scores, eventKinds(s))
	}
}

// ── 振听 ────────────────────────────────────────────────────────

// TestFuritenDiscardWaits 捨て牌振听：自己牌河里有听牌 → 不能荣和。
func TestFuritenDiscardWaits(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 听 5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Discards[1] = []tile.Tile{mustTile(t, "5p")} // seat1 自己打过 5p
	applyDiscard(t, s, 0, "5p")
	if optionKinds(s, 1)[ActRon] {
		t.Fatal("捨て牌振听不应有荣和选项")
	}
	if s.canRon(1, mustTile(t, "5p")) {
		t.Fatal("canRon 应返回 false")
	}
}

// TestFuritenTsumoStillAllowed 振听只限制荣和，自摸仍然可以和牌。
func TestFuritenTsumoStillAllowed(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m1p",
		"123m456m789m11p34p", // seat1 听 5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Furiten[1] = true
	// 手动把 seat1 变成「刚摸到 5p」的打牌阶段
	drawn := mustTile(t, "5p")
	s.Hands[1] = append(s.Hands[1], drawn)
	s.Drawn = &drawn
	s.Turn = 1
	s.Phase = PhaseAwaitDiscard
	s.DrawCount = 2
	s.SeatDraws[1] = 1
	if !s.canTsumo(1) {
		t.Fatal("振听不应影响自摸")
	}
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}
	if countEvents(s, EventTsumo) != 1 {
		t.Fatalf("tsumo 事件数=%d want 1", countEvents(s, EventTsumo))
	}
}

// TestFuritenPonStillAllowed 振听不影响碰/吃等副露。
func TestFuritenPonStillAllowed(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"5p5p123m456m789m1p2p", // seat1 可碰 5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Furiten[1] = true
	applyDiscard(t, s, 0, "5p")
	if optionKinds(s, 1)[ActRon] {
		t.Fatal("振听不应有荣和选项")
	}
	if !optionKinds(s, 1)[ActPon] {
		t.Fatal("振听不应影响碰")
	}
}

// TestFuritenTempClearedOnDraw 同巡振听在自己摸牌后解除。
func TestFuritenTempClearedOnDraw(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 可荣和 5p
		"5p5p123m456m789m1p2p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	if err := s.ResolveTimeout(); err != nil {
		t.Fatalf("ResolveTimeout: %v", err)
	}
	if !s.TempFuriten[1] {
		t.Fatal("pass 荣和后应进入同巡振听")
	}
	if s.canRon(1, mustTile(t, "5p")) {
		t.Fatal("同巡振听期间不能荣和")
	}
	// 模拟 seat1 下一巡摸牌（摸牌会清同巡振听）
	if s.Turn != 1 || s.Phase != PhaseAwaitDraw {
		t.Fatalf("turn/phase=%d/%v，期望 1/await_draw", s.Turn, s.Phase)
	}
	if err := s.Apply(Action{Kind: ActDraw, Seat: 1}); err != nil {
		t.Fatalf("摸牌失败: %v", err)
	}
	if s.TempFuriten[1] {
		t.Fatal("摸牌后同巡振听应解除")
	}
}

// TestFuritenRiichiPermanent 立直后振听：立直者放弃荣和后永久振听。
func TestFuritenRiichiPermanent(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 听 5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.Riichi[1] = true
	s.Ippatsu[1] = true
	applyDiscard(t, s, 0, "5p")
	if !optionKinds(s, 1)[ActRon] {
		t.Fatal("立直者听牌应有荣和选项")
	}
	applyResponse(t, s, 1, ActPass)
	if !s.Furiten[1] {
		t.Fatal("立直者放弃荣和后应永久振听")
	}
	// 摸牌也解除不了永久振听
	s.Turn = 1
	s.Phase = PhaseAwaitDraw
	if err := s.Apply(Action{Kind: ActDraw, Seat: 1}); err != nil {
		t.Fatalf("摸牌失败: %v", err)
	}
	if !s.Furiten[1] {
		t.Fatal("永久振听不应被摸牌解除")
	}
}

// ── 食替禁止 ────────────────────────────────────────────────────

// TestKuikaeChiSameTile 吃后立刻打刚吃的牌 → 非法。
func TestKuikaeChiSameTile(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p", // seat0 打 5p
		"3p4p123m456m789m11p",          // seat1 吃 5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p6p8p9p1z2z3z",
	)
	s.Hands[1] = append(s.Hands[1], mustTile(t, "5p")) // 吃后手里还有一张 5p 可打
	applyDiscard(t, s, 0, "5p")
	chi := Action{Kind: ActChi, Seat: 1, Tile: mustTile(t, "5p"),
		Consumed: s.Pending.Options[1][len(s.Pending.Options[1])-1].Consumed}
	if err := s.Apply(chi); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 1, Tile: mustTile(t, "5p")}); err == nil {
		t.Fatal("吃后打同种牌应被食替禁止拦截")
	}
}

// TestKuikaeChiSuji 吃 5p 后打 2p/8p（筋）→ 非法。
func TestKuikaeChiSuji(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p6p8p9p1z2z3z",
	)
	s.Hands[1] = append(s.Hands[1], mustTile(t, "2p"), mustTile(t, "8p"))
	applyDiscard(t, s, 0, "5p")
	chi := Action{Kind: ActChi, Seat: 1, Tile: mustTile(t, "5p"),
		Consumed: s.Pending.Options[1][len(s.Pending.Options[1])-1].Consumed}
	if err := s.Apply(chi); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	for _, text := range []string{"2p", "8p"} {
		if err := s.Apply(Action{Kind: ActDiscard, Seat: 1, Tile: mustTile(t, text)}); err == nil {
			t.Fatalf("吃后打筋牌 %s 应被食替禁止拦截", text)
		}
	}
}

// TestKuikaePonSameTile 碰后立刻打刚碰的牌 → 非法。
func TestKuikaePonSameTile(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"5p5p123m456m789m1p2p",
		"1m4m7m1s4s7s2p6p8p9p1z2z3z",
	)
	s.Hands[2] = append(s.Hands[2], mustTile(t, "5p")) // 碰后手里还有 5p
	applyDiscard(t, s, 0, "5p")
	var pon Action
	for _, option := range s.Pending.Options[2] {
		if option.Kind == ActPon {
			pon = option
		}
	}
	if pon.Kind != ActPon {
		t.Fatalf("seat2 应有碰选项: %v", s.Pending.Options[2])
	}
	if err := s.Apply(pon); err != nil {
		t.Fatalf("碰失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 2, Tile: mustTile(t, "5p")}); err == nil {
		t.Fatal("碰后打同种牌应被食替禁止拦截")
	}
}

// ── 九种九牌 ────────────────────────────────────────────────────

// TestKyuushuAllowed 九种九牌：配牌第一巡 13 种幺九字牌可流局。
func TestKyuushuAllowed(t *testing.T) {
	s := mustState(t, 7)
	s.Hands[0] = mustHand(t, "1m9m1p9p1s9s1z2z3z4z5z6z7z1m") // 13 种幺九字牌 + 重复 1m
	drawn := s.Hands[0][13]
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Turn = 0
	s.SeatDraws[0] = 1
	s.Calls = 0
	s.Melds = [4][]Meld{}
	if !s.canKyuushu(0) {
		t.Fatal("应满足九种九牌条件")
	}
	if err := s.Apply(Action{Kind: ActKyuushu, Seat: 0}); err != nil {
		t.Fatalf("九种九牌失败: %v", err)
	}
	if s.Phase != PhaseRoundEnd {
		t.Fatalf("phase=%v want round_end", s.Phase)
	}
	if !hasEventKind(s, EventRyuukyoku) {
		t.Fatal("应产生 ryuukyoku 事件")
	}
	// 途中流局不做听牌料，点数不变
	if s.Scores != [4]int{25000, 25000, 25000, 25000} {
		t.Fatalf("九种九牌不应改变点数: %v", s.Scores)
	}
	if !s.LastDealerRepeat {
		t.Fatal("九种九牌亲家应连庄")
	}
}

// TestKyuushuRejectedWhenNotEnoughKinds 幺九字牌不足 9 种时不能九种九牌。
func TestKyuushuRejectedWhenNotEnoughKinds(t *testing.T) {
	s := mustState(t, 7)
	s.Hands[0] = mustHand(t, "1m9m1p9p1s9s1z2z123m456m78m")
	drawn := s.Hands[0][13]
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Turn = 0
	s.SeatDraws[0] = 1
	s.Calls = 0
	if s.canKyuushu(0) {
		t.Fatal("8 种幺九字牌不应允许九种九牌")
	}
	if err := s.Apply(Action{Kind: ActKyuushu, Seat: 0}); err == nil {
		t.Fatal("不满足条件时 Apply 应报错")
	}
}
