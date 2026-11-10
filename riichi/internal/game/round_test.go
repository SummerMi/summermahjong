package game

import (
	"encoding/json"
	"testing"

	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// ── 立直 ────────────────────────────────────────────────────────

// TestRiichiSucceeds 门清听牌且点数足够时可立直：扣 1000、立直棒 +1、一发标记、事件正确。
func TestRiichiSucceeds(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 1, "123m456m789m11p34p2p") // 打 2p 后仍听 5p
	if err := s.Apply(Action{Kind: ActRiichi, Seat: 1, Tile: mustTile(t, "2p")}); err != nil {
		t.Fatalf("立直失败: %v", err)
	}
	if !s.Riichi[1] || !s.Ippatsu[1] {
		t.Fatalf("riichi=%v ippatsu=%v，期望都为 true", s.Riichi[1], s.Ippatsu[1])
	}
	if s.RiichiStix != 1 || s.Scores[1] != 24000 {
		t.Fatalf("立直棒=%d scores[1]=%d，期望 1/24000", s.RiichiStix, s.Scores[1])
	}
	if countEvents(s, EventRiichi) != 1 || countEvents(s, EventDiscard) != 1 {
		t.Fatalf("事件=%v，期望 riichi + discard", eventKinds(s))
	}
	// 立直宣言牌打 2p（不是摸到的牌）也允许：宣言后手牌固定
	if s.Phase != PhaseAwaitDraw {
		t.Fatalf("立直后 phase=%v want await_draw（自动 pass 模式）", s.Phase)
	}
}

// TestRiichiRejectedWithMeld 有副露不能立直。
func TestRiichiRejectedWithMeld(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 1, "123m456m789m11p34p2p")
	s.Melds[1] = []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
	}}}
	if err := s.Apply(Action{Kind: ActRiichi, Seat: 1, Tile: mustTile(t, "2p")}); err == nil {
		t.Fatal("有副露时立直应失败")
	}
}

// TestRiichiRejectedLowScore 点数不足 1000 不能立直。
func TestRiichiRejectedLowScore(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 1, "123m456m789m11p34p2p")
	s.Scores[1] = 500
	if err := s.Apply(Action{Kind: ActRiichi, Seat: 1, Tile: mustTile(t, "2p")}); err == nil {
		t.Fatal("点数不足时立直应失败")
	}
}

// TestRiichiRejectedNotTenpai 打牌后不听牌不能立直。
func TestRiichiRejectedNotTenpai(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 1, "1m4m7m1s4s7s2p5p8p1z2z3z5s9p")
	if err := s.Apply(Action{Kind: ActRiichi, Seat: 1, Tile: mustTile(t, "9p")}); err == nil {
		t.Fatal("不听牌时立直应失败")
	}
}

// TestRiichiOnlyTsumogiriAfterDeclare 立直后只能摸切（打非摸牌非法）。
func TestRiichiOnlyTsumogiriAfterDeclare(t *testing.T) {
	s := mustState(t, 42)
	s.Riichi[1] = true
	setActor(t, s, 1, "123m456m789m11p34p5p")
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 1, Tile: mustTile(t, "1m")}); err == nil {
		t.Fatal("立直后打非摸牌应失败")
	}
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 1, Tile: mustTile(t, "5p")}); err != nil {
		t.Fatalf("立直后摸切失败: %v", err)
	}
}

// TestRiichiLegalActionsOnlyTsumogiri 立直后 Legal 只给摸切（这张不是和牌）。
func TestRiichiLegalActionsOnlyTsumogiri(t *testing.T) {
	s := mustState(t, 42)
	s.Riichi[1] = true
	setActor(t, s, 1, "123m456m789m11p34p7p")
	legal := s.Legal()
	if len(legal) != 1 || legal[0].Kind != ActDiscard || legal[0].Tile != mustTile(t, "7p") {
		t.Fatalf("立直后 Legal=%v，应只有摸切 7p", legal)
	}
}

// ── 杠 ──────────────────────────────────────────────────────────

// TestAnkanRemovesFourAndDrawsRinshan 暗杠：副露 4 张、翻宝牌、摸岭上牌。
func TestAnkanRemovesFourAndDrawsRinshan(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 0, "1z1z1z1z123m456m78m9p9p")
	doraBefore := len(s.Wall.DoraIndicators())

	if err := s.Apply(Action{Kind: ActKanAnkan, Seat: 0, Tile: mustTile(t, "1z")}); err != nil {
		t.Fatalf("暗杠失败: %v", err)
	}
	if len(s.Melds[0]) != 1 || s.Melds[0][0].Kind != MeldKanAnkan || len(s.Melds[0][0].Tiles) != 4 {
		t.Fatalf("暗杠副露=%v", s.Melds[0])
	}
	if len(s.Hands[0]) != 11 {
		t.Fatalf("暗杠后手牌=%d want 11（14-3*1）", len(s.Hands[0]))
	}
	if s.Drawn == nil {
		t.Fatal("杠后应从王牌摸岭上牌")
	}
	if len(s.Wall.DoraIndicators()) != doraBefore+1 {
		t.Fatalf("宝牌指示牌=%d want %d", len(s.Wall.DoraIndicators()), doraBefore+1)
	}
	if countEvents(s, EventKan) != 1 || countEvents(s, EventDoraReveal) != 1 {
		t.Fatalf("事件=%v，期望 kan + dora_reveal", eventKinds(s))
	}
	if s.Phase != PhaseAwaitDiscard || s.Turn != 0 {
		t.Fatalf("杠后 phase/turn=%v/%d", s.Phase, s.Turn)
	}
}

// TestAnkanRejectedWithoutFour 不足 4 张不能暗杠。
func TestAnkanRejectedWithoutFour(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 0, "1z1z1z123m456m789m1p1p")
	if err := s.Apply(Action{Kind: ActKanAnkan, Seat: 0, Tile: mustTile(t, "1z")}); err == nil {
		t.Fatal("3 张不能暗杠")
	}
}

// TestDaiminkanFromDiscard 大明杠：牌河移除、副露 4 张、翻宝牌、摸岭上、轮转到杠者。
func TestDaiminkanFromDiscard(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"5p5p5p123m456m789m9p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	var kan Action
	for _, option := range s.Pending.Options[2] {
		if option.Kind == ActKanDaiminkan {
			kan = option
		}
	}
	if kan.Kind != ActKanDaiminkan {
		t.Fatalf("seat2 应可大明杠: %v", s.Pending.Options[2])
	}
	if err := s.Apply(kan); err != nil {
		t.Fatalf("大明杠失败: %v", err)
	}
	if len(s.Melds[2]) != 1 || len(s.Melds[2][0].Tiles) != 4 {
		t.Fatalf("大明杠副露=%v", s.Melds[2])
	}
	if len(s.Hands[2]) != 11 {
		t.Fatalf("大明杠后手牌=%d want 11", len(s.Hands[2]))
	}
	if len(s.Discards[0]) != 0 {
		t.Fatal("被杠的牌应从牌河移除")
	}
	if s.Turn != 2 || s.Phase != PhaseAwaitDiscard {
		t.Fatalf("大明杠后 turn/phase=%d/%v", s.Turn, s.Phase)
	}
	if countEvents(s, EventKan) != 1 || countEvents(s, EventDoraReveal) != 1 {
		t.Fatalf("事件=%v", eventKinds(s))
	}
}

// TestShouminkanCompletedWhenNoChankan 加杠无人抢杠：副露升级为 4 张并摸岭上。
func TestShouminkanCompletedWhenNoChankan(t *testing.T) {
	pon5p := []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
	}, From: 1}}
	s := scenarioWithMelds(t, "123m456m789m1z5p", pon5p,
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	drawn := mustTile(t, "5p")
	s.Drawn = &drawn

	if err := s.Apply(Action{Kind: ActKanShouminkan, Seat: 0, Tile: drawn}); err != nil {
		t.Fatalf("加杠失败: %v", err)
	}
	// 无人能抢杠 → 自动（手动模式下手动 pass）完成
	if s.Pending != nil {
		if err := s.ResolveTimeout(); err != nil {
			t.Fatalf("抢杠超时: %v", err)
		}
	}
	if len(s.Melds[0]) != 1 || s.Melds[0][0].Kind != MeldKanShouminkan || len(s.Melds[0][0].Tiles) != 4 {
		t.Fatalf("加杠后副露=%v", s.Melds[0])
	}
	if countEvents(s, EventKan) != 1 || countEvents(s, EventDoraReveal) != 1 {
		t.Fatalf("事件=%v", eventKinds(s))
	}
	if s.Phase != PhaseAwaitDiscard || s.Turn != 0 || s.Drawn == nil {
		t.Fatalf("加杠后 phase/turn/drawn=%v/%d/%v", s.Phase, s.Turn, s.Drawn)
	}
}

// TestChankanRon 抢杠：加杠的第四张可以被荣和（chankan=true，加杠者放铳）。
func TestChankanRon(t *testing.T) {
	pon5p := []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
	}, From: 1}}
	s := scenarioWithMelds(t, "123m456m789m1z5p", pon5p,
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"123m456m789m11p34p", // seat2 听 5p，可抢杠
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	drawn := mustTile(t, "5p")
	s.Drawn = &drawn
	s.Turn = 0
	s.Phase = PhaseAwaitDiscard

	if err := s.Apply(Action{Kind: ActKanShouminkan, Seat: 0, Tile: drawn}); err != nil {
		t.Fatalf("加杠失败: %v", err)
	}
	if s.Phase != PhaseAwaitResponse {
		t.Fatalf("抢杠窗口 phase=%v want await_response", s.Phase)
	}
	if !optionKinds(s, 2)[ActRon] {
		t.Fatal("seat2 应有抢杠荣和选项")
	}
	applyRon(t, s, 2, "5p")
	// 抢杠成立时加杠不完成：不产生 kan 事件，只有 ron + round_end
	if countEvents(s, EventRon) != 1 {
		t.Fatalf("事件=%v，期望抢杠荣和成立", eventKinds(s))
	}
	if countEvents(s, EventKan) != 0 {
		t.Fatalf("抢杠成立后不应完成加杠（kan 事件=%d）", countEvents(s, EventKan))
	}
	if countEvents(s, EventDoraReveal) != 0 {
		t.Fatalf("抢杠成立后不应翻新宝牌（dora_reveal=%d）", countEvents(s, EventDoraReveal))
	}
	// 找一个 ron 事件（round_end 之前）
	var ronEvent Event
	for _, ev := range s.Events {
		if ev.Kind == EventRon {
			ronEvent = ev
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(ronEvent.Payload, &payload); err != nil {
		t.Fatalf("解析 payload: %v", err)
	}
	if payload["chankan"] != true {
		t.Fatalf("payload=%v，期望 chankan=true", payload)
	}
	// 抢杠 1 翻 + 一气 2 翻 + 平和 1 翻 = 4 翻 30 符，荣和 7700
	if s.Scores[2] != 32700 || s.Scores[0] != 17300 {
		t.Fatalf("抢杠点数=%v，期望 seat2=32700 seat0=17300", s.Scores)
	}
}

// ── 和了结算 / 局与场 ───────────────────────────────────────────

// setActor 把某座位设为「刚摸牌、等待打牌」，并覆盖其手牌（14 张，最后一张视为摸牌）。
func setActor(t *testing.T, s *State, seat Seat, handText string) {
	t.Helper()
	hand := mustHand(t, handText)
	if len(hand) != 14-3*len(s.Melds[seat]) {
		t.Fatalf("seat%d 手牌应为 %d 张，实际 %d", seat, 14-3*len(s.Melds[seat]), len(hand))
	}
	s.Hands[seat] = hand
	drawn := hand[len(hand)-1]
	s.Drawn = &drawn
	s.Turn = seat
	s.Phase = PhaseAwaitDiscard
	s.DrawCount++
	s.SeatDraws[seat]++
}

// TestTsumoDealerRepeat 亲家自摸：连庄、本场 +1、立直棒归和了者、事件序列完整。
func TestTsumoDealerRepeat(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 0, "123m456m789m11p234p") // 摸 4p 自摸
	s.RiichiStix = 2
	s.Scores[0] -= 2000 // 模拟两根立直棒已扣除
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 0}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}
	if s.Phase != PhaseRoundEnd {
		t.Fatalf("phase=%v want round_end", s.Phase)
	}
	if !s.LastDealerRepeat {
		t.Fatal("亲家自摸应连庄")
	}
	if !hasEventKind(s, EventTsumo) || !hasEventKind(s, EventRoundEnd) {
		t.Fatalf("事件=%v", eventKinds(s))
	}
	// 一气 2 翻 + 平和 1 翻 + 门清自摸 1 翻 = 4 翻 20 符：亲家自摸每家 2600（共 7800）+2000 立直棒
	if s.Scores[0] != 23000+7800+2000 {
		t.Fatalf("scores[0]=%d，期望 32800", s.Scores[0])
	}
	if s.RiichiStix != 0 {
		t.Fatalf("riichi sticks=%d want 0", s.RiichiStix)
	}

	// 连庄：StartNextRound 后仍是东 1 局 1 本场
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	if s.Dealer != 0 || s.Honba != 1 || s.Kyoku != 1 || s.RoundWind != East {
		t.Fatalf("连庄后 dealer=%d honba=%d kyoku=%d wind=%v", s.Dealer, s.Honba, s.Kyoku, s.RoundWind)
	}
	if s.Phase != PhaseAwaitDraw || s.Turn != 0 {
		t.Fatalf("新一局 phase/turn=%v/%d", s.Phase, s.Turn)
	}
	if len(s.Events) == 0 || s.Events[len(s.Events)-1].Kind != EventRoundStart {
		t.Fatal("新一局应产生 round_start 事件")
	}
}

// TestTsumoNonDealerRotates 闲家自摸：亲家轮转、本场清零、局数 +1。
func TestTsumoNonDealerRotates(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 1, "123m456m789m11p234p")
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}
	if s.LastDealerRepeat {
		t.Fatal("闲家自摸不应连庄")
	}
	// 4 翻 20 符闲家自摸：亲家 2600 + 其他两家各 1300，共 5200
	if s.Scores[1] != 30200 {
		t.Fatalf("scores[1]=%d，期望 30200", s.Scores[1])
	}
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	if s.Dealer != 1 || s.Honba != 0 || s.Kyoku != 2 {
		t.Fatalf("轮转后 dealer=%d honba=%d kyoku=%d", s.Dealer, s.Honba, s.Kyoku)
	}
}

// playTsumogiri 用「有摸就摸、摸到什么打什么」的脚本一直打到本局结束。
func playTsumogiri(t *testing.T, s *State, maxSteps int) []Action {
	t.Helper()
	actions := make([]Action, 0, maxSteps)
	for i := 0; i < maxSteps; i++ {
		switch s.Phase {
		case PhaseAwaitDraw:
			draw := Action{Kind: ActDraw, Seat: s.Turn}
			if err := s.Apply(draw); err != nil {
				if s.Phase == PhaseRoundEnd {
					return actions
				}
				t.Fatalf("摸牌失败: %v", err)
			}
			actions = append(actions, draw)
		case PhaseAwaitDiscard:
			seat := s.Turn
			tl := *s.Drawn
			if err := s.Apply(Action{Kind: ActDiscard, Seat: seat, Tile: tl}); err != nil {
				t.Fatalf("打牌失败: %v", err)
			}
			actions = append(actions, Action{Kind: ActDiscard, Seat: seat, Tile: tl})
		case PhaseAwaitResponse:
			if err := s.ResolveTimeout(); err != nil {
				t.Fatalf("响应超时失败: %v", err)
			}
		default:
			return actions
		}
	}
	return actions
}

// TestScriptedRoundFromDealToRyuukyoku 整局脚本：从配牌打到本局结束，点数守恒、事件完整、
// 回放逐字段一致（seed 42 的正常走向是荒牌流局；若意外出现和了则同样验证结算）。
func TestScriptedRoundFromDealToRyuukyoku(t *testing.T) {
	s := mustState(t, 42)
	actions := playTsumogiri(t, s, 400)
	if s.Phase != PhaseRoundEnd {
		t.Fatalf("phase=%v want round_end", s.Phase)
	}
	if !hasEventKind(s, EventRyuukyoku) && !hasEventKind(s, EventTsumo) && !hasEventKind(s, EventRon) {
		t.Fatalf("事件=%v，期望本局以流局或和了结束", eventKinds(s))
	}
	// 点数守恒：total + 1000*场上立直棒 = 100000
	total := 0
	for _, score := range s.Scores {
		total += score
	}
	if total+1000*s.RiichiStix != 4*25000 {
		t.Fatalf("点数守恒失败: total=%d sticks=%d", total, s.RiichiStix)
	}
	// 回放一致性
	replay, err := Replay(42, DefaultRuleset(), actions)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	assertStateEqual(t, s, replay)
}

// playShantenScript 用「向听最小化」的简单脚本打整局（有摸就摸、和了就自摸、否则打最不影响牌效的牌）。
func playShantenScript(t *testing.T, s *State, maxActions int) []Action {
	t.Helper()
	actions := make([]Action, 0, maxActions)
	for len(actions) < maxActions {
		switch s.Phase {
		case PhaseAwaitDraw:
			action := Action{Kind: ActDraw, Seat: s.Turn}
			if err := s.Apply(action); err != nil {
				if s.Phase == PhaseRoundEnd {
					return actions // 荒牌流局
				}
				t.Fatalf("摸牌失败: %v", err)
			}
			actions = append(actions, action)
		case PhaseAwaitDiscard:
			seat := s.Turn
			if s.canTsumo(seat) {
				action := Action{Kind: ActTsumo, Seat: seat, Tile: *s.Drawn}
				if err := s.Apply(action); err != nil {
					t.Fatalf("自摸失败: %v", err)
				}
				actions = append(actions, action)
				return actions
			}
			hand := s.Hands[seat]
			bestIdx, bestShanten := -1, 99
			for i := range hand {
				rest := make([]tile.Tile, 0, len(hand)-1)
				rest = append(rest, hand[:i]...)
				rest = append(rest, hand[i+1:]...)
				sh := rules.Shanten(rest, len(s.Melds[seat]))
				if sh < bestShanten {
					bestShanten, bestIdx = sh, i
				}
			}
			if bestIdx < 0 {
				return actions
			}
			action := Action{Kind: ActDiscard, Seat: seat, Tile: hand[bestIdx]}
			if err := s.Apply(action); err != nil {
				t.Fatalf("打牌失败: %v", err)
			}
			actions = append(actions, action)
		case PhaseAwaitResponse:
			if err := s.ResolveTimeout(); err != nil {
				t.Fatalf("响应超时失败: %v", err)
			}
		default:
			return actions
		}
	}
	return actions
}

// TestScriptedRoundFromDealToTsumo 整局脚本：从配牌用向听最小化脚本打到自摸和了，
// 再用同一动作序列 Replay，验证逐字段一致（这是「能真打一局」的核心证据）。
func TestScriptedRoundFromDealToTsumo(t *testing.T) {
	const maxSeed = 120
	for seed := int64(1); seed <= maxSeed; seed++ {
		live := mustState(t, seed)
		actions := playShantenScript(t, live, 400)
		if !hasEventKind(live, EventTsumo) {
			continue
		}
		t.Logf("seed=%d 从配牌自摸和了：动作数=%d 事件数=%d 分数=%v",
			seed, len(actions), live.Seq, live.Scores)
		replay, err := Replay(seed, DefaultRuleset(), actions)
		if err != nil {
			t.Fatalf("Replay(seed=%d): %v", seed, err)
		}
		assertStateEqual(t, live, replay)
		if !hasEventKind(live, EventRoundEnd) {
			t.Fatal("和了后应有 round_end 事件")
		}
		return
	}
	t.Fatalf("前 %d 个 seed 的向听脚本均未自摸（应至少有一个可复现的和了样本）", maxSeed)
}

// TestRyuukyokuTenpaiPayments 荒牌流局的听牌料：分配总额 3000，方向正确。
func TestRyuukyokuTenpaiPayments(t *testing.T) {
	s := mustState(t, 42)
	playTsumogiri(t, s, 400)
	if !hasEventKind(s, EventRyuukyoku) {
		t.Fatalf("事件=%v，期望 ryuukyoku", eventKinds(s))
	}
	var ryuukyoku Event
	for _, ev := range s.Events {
		if ev.Kind == EventRyuukyoku {
			ryuukyoku = ev
		}
	}
	var payload struct {
		TenpaiSeats []int  `json:"tenpai_seats"`
		ScoreDeltas [4]int `json:"score_deltas"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(ryuukyoku.Payload, &payload); err != nil {
		t.Fatalf("解析 ryuukyoku payload: %v", err)
	}
	if payload.Reason != RyuukyokuHaitei {
		t.Fatalf("reason=%q want %q", payload.Reason, RyuukyokuHaitei)
	}
	sum := 0
	for _, delta := range payload.ScoreDeltas {
		sum += delta
	}
	if sum != 0 {
		t.Fatalf("听牌料总和应为 0，实际 %d (%v)", sum, payload.ScoreDeltas)
	}
	tenpaiCount := len(payload.TenpaiSeats)
	if tenpaiCount > 0 && tenpaiCount < 4 {
		if tenpaiCount == 1 || tenpaiCount == 2 || tenpaiCount == 3 {
			// 听牌者收正分，未听者付负分
			for seat := Seat(0); seat < 4; seat++ {
				isTenpai := false
				for _, ts := range payload.TenpaiSeats {
					if ts == int(seat) {
						isTenpai = true
					}
				}
				if isTenpai && payload.ScoreDeltas[seat] <= 0 {
					t.Fatalf("听牌者 seat%d delta=%d 应为正", seat, payload.ScoreDeltas[seat])
				}
				if !isTenpai && payload.ScoreDeltas[seat] >= 0 {
					t.Fatalf("未听者 seat%d delta=%d 应为负", seat, payload.ScoreDeltas[seat])
				}
			}
		}
	}
}

// TestTobiEndsGame トビ終了：有人点数为负时下一局结束整场。
func TestTobiEndsGame(t *testing.T) {
	s := mustState(t, 42)
	s.Phase = PhaseRoundEnd
	s.Scores[2] = -100
	if !s.gonnaEnd() {
		t.Fatal("负点数应触发トビ終了")
	}
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	if s.Phase != PhaseGameEnd {
		t.Fatalf("phase=%v want game_end", s.Phase)
	}
	if !hasEventKind(s, EventGameEnd) {
		t.Fatal("应产生 game_end 事件")
	}
	if !s.GameOver() {
		t.Fatal("GameOver 应为 true")
	}
}

// TestSouth4EndsGame 南 4 结束后整场结束。
func TestSouth4EndsGame(t *testing.T) {
	s := mustState(t, 42)
	s.RoundWind = South
	s.Kyoku = 4
	s.Phase = PhaseRoundEnd
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	if s.Phase != PhaseGameEnd {
		t.Fatalf("南 4 后 phase=%v want game_end", s.Phase)
	}
}

// TestStartNextRoundWrongPhase 非 RoundEnd 阶段不能开局。
func TestStartNextRoundWrongPhase(t *testing.T) {
	s := mustState(t, 42)
	if err := s.StartNextRound(); err == nil {
		t.Fatal("非 round_end 阶段 StartNextRound 应报错")
	}
}

// TestApplyRejectedAfterRoundEnd 本局/整场结束后 Apply 应报错。
func TestApplyRejectedAfterRoundEnd(t *testing.T) {
	s := mustState(t, 42)
	s.Phase = PhaseRoundEnd
	if err := s.Apply(Action{Kind: ActDraw, Seat: 0}); err == nil {
		t.Fatal("round_end 阶段 Apply 应报错")
	}
	s.Phase = PhaseGameEnd
	if err := s.Apply(Action{Kind: ActDraw, Seat: 0}); err == nil {
		t.Fatal("game_end 阶段 Apply 应报错")
	}
}

// ── 回放一致性 ──────────────────────────────────────────────────

// TestReplayManualResponseScenario 手动响应模式的动作序列（含 draw / discard / 响应 pass）
// 从实时对局录制后，用同样的 Options 回放，状态与事件逐字段一致。
func TestReplayManualResponseScenario(t *testing.T) {
	live := mustManualState(t, 7)
	actions := playManualPassScript(t, live, 60)
	if live.Phase != PhaseAwaitDraw && live.Phase != PhaseAwaitDiscard && live.Phase != PhaseRoundEnd {
		t.Fatalf("脚本终态 phase=%v 非预期", live.Phase)
	}
	replay, err := ReplayWithOptions(7, DefaultRuleset(), actions, Options{ManualResponses: true})
	if err != nil {
		t.Fatalf("ReplayWithOptions: %v", err)
	}
	assertStateEqual(t, live, replay)
}

// playManualPassScript 以手动响应模式打若干步：摸牌、摸切、遇到响应窗口就全员 pass。
// 返回与实时完全一致的动作序列（回放需要把 draw/响应 pass 也记录下来）。
func playManualPassScript(t *testing.T, s *State, maxActions int) []Action {
	t.Helper()
	actions := make([]Action, 0, maxActions)
	for len(actions) < maxActions {
		switch s.Phase {
		case PhaseAwaitDraw:
			action := Action{Kind: ActDraw, Seat: s.Turn}
			if err := s.Apply(action); err != nil {
				t.Fatalf("摸牌失败: %v", err)
			}
			actions = append(actions, action)
		case PhaseAwaitDiscard:
			seat := s.Turn
			tl := *s.Drawn
			action := Action{Kind: ActDiscard, Seat: seat, Tile: tl}
			if err := s.Apply(action); err != nil {
				t.Fatalf("打牌失败: %v", err)
			}
			actions = append(actions, action)
		case PhaseAwaitResponse:
			for seat := Seat(0); seat < 4; seat++ {
				if s.Pending == nil || s.Pending.Decided[seat] {
					continue
				}
				if err := s.Apply(Action{Kind: ActPass, Seat: seat}); err != nil {
					t.Fatalf("响应 pass 失败: %v", err)
				}
				actions = append(actions, Action{Kind: ActPass, Seat: seat})
			}
		default:
			return actions
		}
	}
	return actions
}

// TestReplayAcrossRounds 跨局回放：实时调用 StartNextRound 后继续打，回放自动跨局复现。
func TestReplayAcrossRounds(t *testing.T) {
	s := mustState(t, 7)
	actions := playTsumogiri(t, s, 400) // 第一局打到结束
	if s.Phase != PhaseRoundEnd {
		t.Fatalf("第一局 phase=%v want round_end", s.Phase)
	}
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	// 第二局再打几步（摸打循环）
	for step := 0; step < 8; step++ {
		if s.Phase == PhaseAwaitDraw {
			draw := Action{Kind: ActDraw, Seat: s.Turn}
			if err := s.Apply(draw); err != nil {
				t.Fatalf("摸牌: %v", err)
			}
			actions = append(actions, draw)
			continue
		}
		if s.Phase == PhaseAwaitDiscard {
			seat := s.Turn
			tl := *s.Drawn
			if err := s.Apply(Action{Kind: ActDiscard, Seat: seat, Tile: tl}); err != nil {
				t.Fatalf("打牌: %v", err)
			}
			actions = append(actions, Action{Kind: ActDiscard, Seat: seat, Tile: tl})
			continue
		}
		break
	}
	replay, err := Replay(7, DefaultRuleset(), actions)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	assertStateEqual(t, s, replay)
	if replay.Kyoku != s.Kyoku || replay.Dealer != s.Dealer || replay.Honba != s.Honba {
		t.Fatalf("跨局回放局信息不一致: live=%d/%d/%d replay=%d/%d/%d",
			s.Kyoku, s.Dealer, s.Honba, replay.Kyoku, replay.Dealer, replay.Honba)
	}
}

// assertStateEqual 逐字段比较实时状态与回放状态。
func assertStateEqual(t *testing.T, live, replay *State) {
	t.Helper()
	if live.Seq != replay.Seq {
		t.Fatalf("seq 不一致: live=%d replay=%d", live.Seq, replay.Seq)
	}
	if live.Phase != replay.Phase || live.Turn != replay.Turn {
		t.Fatalf("phase/turn 不一致: %v/%d vs %v/%d",
			live.Phase, live.Turn, replay.Phase, replay.Turn)
	}
	if live.Dealer != replay.Dealer || live.Kyoku != replay.Kyoku ||
		live.Honba != replay.Honba || live.RoundWind != replay.RoundWind ||
		live.RiichiStix != replay.RiichiStix {
		t.Fatalf("局信息不一致: live 庄%d 局%d 本%d 风%v 棒%d vs replay 庄%d 局%d 本%d 风%v 棒%d",
			live.Dealer, live.Kyoku, live.Honba, live.RoundWind, live.RiichiStix,
			replay.Dealer, replay.Kyoku, replay.Honba, replay.RoundWind, replay.RiichiStix)
	}
	if live.Scores != replay.Scores {
		t.Fatalf("点数不一致: %v vs %v", live.Scores, replay.Scores)
	}
	if live.Riichi != replay.Riichi || live.Furiten != replay.Furiten ||
		live.TempFuriten != replay.TempFuriten || live.Ippatsu != replay.Ippatsu {
		t.Fatal("立直/振听/一发标记不一致")
	}
	if live.Wall.Remaining() != replay.Wall.Remaining() {
		t.Fatalf("牌山剩余不一致: %d vs %d", live.Wall.Remaining(), replay.Wall.Remaining())
	}
	for seat := 0; seat < 4; seat++ {
		if len(live.Hands[seat]) != len(replay.Hands[seat]) {
			t.Fatalf("seat%d 手牌数不一致", seat)
		}
		for i := range live.Hands[seat] {
			if live.Hands[seat][i] != replay.Hands[seat][i] {
				t.Fatalf("seat%d 手牌[%d]不一致", seat, i)
			}
		}
		for i := range live.Discards[seat] {
			if live.Discards[seat][i] != replay.Discards[seat][i] {
				t.Fatalf("seat%d 牌河[%d]不一致", seat, i)
			}
		}
	}
	if len(live.Melds) != len(replay.Melds) {
		t.Fatal("副露数量不一致")
	}
	for seat := 0; seat < 4; seat++ {
		if len(live.Melds[seat]) != len(replay.Melds[seat]) {
			t.Fatalf("seat%d 副露数不一致", seat)
		}
		for i := range live.Melds[seat] {
			if live.Melds[seat][i].Kind != replay.Melds[seat][i].Kind ||
				live.Melds[seat][i].From != replay.Melds[seat][i].From ||
				len(live.Melds[seat][i].Tiles) != len(replay.Melds[seat][i].Tiles) {
				t.Fatalf("seat%d 副露[%d]不一致", seat, i)
			}
		}
	}
	if live.Pending != nil || replay.Pending != nil {
		t.Fatal("终态不应仍有 pending 响应窗口")
	}
	if len(live.Events) != len(replay.Events) {
		t.Fatalf("事件数不一致: %d vs %d", len(live.Events), len(replay.Events))
	}
	for i := range live.Events {
		if !eventsEqual(live.Events[i], replay.Events[i]) {
			t.Fatalf("事件[%d]不一致: %+v vs %+v", i, live.Events[i], replay.Events[i])
		}
	}
}

// ── 计分占位实现 / 宝牌 / 工具函数 ──────────────────────────────

// TestPlaceholderScorer 占位计分：1 翻 30 符，亲闲/荣和/自摸/本场都正确。
func TestPlaceholderScorer(t *testing.T) {
	scorer := placeholderScorer{}

	ronChild := WinContext{Seat: 1, DealerSeat: 0, RonTarget: 0, Dealer: false, SeatWind: 1}
	result, err := scorer.ScoreWin(ronChild)
	if err != nil {
		t.Fatalf("ScoreWin: %v", err)
	}
	if result.Han != 1 || result.Fu != 30 || result.Deltas[1] != 1000 || result.Deltas[0] != -1000 {
		t.Fatalf("闲家荣和 deltas=%v han=%d fu=%d", result.Deltas, result.Han, result.Fu)
	}

	ronDealer := WinContext{Seat: 0, DealerSeat: 0, RonTarget: 1, Dealer: true}
	result, _ = scorer.ScoreWin(ronDealer)
	if result.Deltas[0] != 1500 || result.Deltas[1] != -1500 {
		t.Fatalf("亲家荣和 deltas=%v", result.Deltas)
	}

	tsumoDealer := WinContext{Seat: 0, DealerSeat: 0, Tsumo: true, Dealer: true}
	result, _ = scorer.ScoreWin(tsumoDealer)
	if result.Deltas[0] != 1500 || result.Deltas[1] != -500 || result.Deltas[2] != -500 {
		t.Fatalf("亲家自摸 deltas=%v", result.Deltas)
	}

	tsumoChild := WinContext{Seat: 1, DealerSeat: 0, Tsumo: true, SeatWind: 1}
	result, _ = scorer.ScoreWin(tsumoChild)
	if result.Deltas[1] != 1100 || result.Deltas[0] != -500 {
		t.Fatalf("闲家自摸 deltas=%v", result.Deltas)
	}

	withHonba := WinContext{Seat: 1, DealerSeat: 0, RonTarget: 0, Dealer: false, SeatWind: 1, Honba: 2}
	result, _ = scorer.ScoreWin(withHonba)
	if result.Deltas[1] != 1600 {
		t.Fatalf("2 本场荣和 deltas=%v，期望 +1600", result.Deltas)
	}
}

// TestDoraKindFromIndicator 宝牌指示牌→宝牌：数牌循环、风牌、三元牌。
func TestDoraKindFromIndicator(t *testing.T) {
	cases := map[tile.Kind]tile.Kind{
		tile.Man1:  tile.Man2,
		tile.Man9:  tile.Man1,
		tile.Pin5:  tile.Pin6,
		tile.Sou9:  tile.Sou1,
		tile.East:  tile.South,
		tile.North: tile.East,
		tile.Haku:  tile.Hatsu,
		tile.Hatsu: tile.Chun,
		tile.Chun:  tile.Haku,
	}
	for indicator, want := range cases {
		if got := doraKindFromIndicator(indicator); got != want {
			t.Fatalf("doraKindFromIndicator(%v)=%v want %v", indicator, got, want)
		}
	}
	if dora := doraKinds([]tile.Tile{{Kind: tile.Man1}, {Kind: tile.Chun}}); len(dora) != 2 ||
		dora[0].Kind != tile.Man2 || dora[1].Kind != tile.Haku {
		t.Fatalf("doraKinds=%v", dora)
	}
}

// TestObserveResponseOptions 响应窗口里 Observation.Responses 给出该座位的响应选项。
func TestObserveResponseOptions(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 可荣和
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	obs := s.Observe(1)
	found := false
	for _, action := range obs.Responses {
		if action.Kind == ActRon {
			found = true
		}
	}
	if !found {
		t.Fatalf("seat1 Observation.Responses=%v，应包含荣和", obs.Responses)
	}
	if len(s.Observe(3).Responses) != 0 {
		t.Fatal("无响应选项的座位 Responses 应为空")
	}
}

// TestTotalTilesConservedWithMeld 副露/杠后总牌数守恒（相对鸣牌前）。
func TestTotalTilesConservedWithMeld(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p", // seat1 吃 5p
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	before := s.TotalTiles()
	applyDiscard(t, s, 0, "5p")
	chi := Action{Kind: ActChi, Seat: 1, Tile: mustTile(t, "5p"),
		Consumed: s.Pending.Options[1][len(s.Pending.Options[1])-1].Consumed}
	if err := s.Apply(chi); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	if after := s.TotalTiles(); after != before {
		t.Fatalf("鸣牌后总牌数=%d want %d", after, before)
	}
}

// TestPhaseString Phase.String 覆盖所有阶段。
func TestPhaseString(t *testing.T) {
	cases := map[Phase]string{
		PhaseAwaitDiscard:  "await_discard",
		PhaseAwaitDraw:     "await_draw",
		PhaseRoundEnd:      "round_end",
		PhaseGameEnd:       "game_end",
		PhaseAwaitResponse: "await_response",
	}
	for phase, want := range cases {
		if got := phase.String(); got != want {
			t.Fatalf("Phase(%d).String()=%q want %q", phase, got, want)
		}
	}
}

// TestStartTimeSeedDeterminism 相同 seed 的两局初始状态一致（replay 的基础）。
func TestStartTimeSeedDeterminism(t *testing.T) {
	a := mustState(t, 12345)
	b := mustState(t, 12345)
	for seat := 0; seat < 4; seat++ {
		if len(a.Hands[seat]) != len(b.Hands[seat]) {
			t.Fatalf("seat%d 手牌数不一致", seat)
		}
		for i := range a.Hands[seat] {
			if a.Hands[seat][i] != b.Hands[seat][i] {
				t.Fatalf("seat%d 手牌不一致", seat)
			}
		}
	}
	if a.Dealer != b.Dealer || a.Kyoku != b.Kyoku || a.Phase != b.Phase {
		t.Fatal("初始局信息不一致")
	}

}
