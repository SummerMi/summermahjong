package game

import (
	"encoding/json"
	"testing"

	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// ── RulesScorer 金标测试：game 结算 == rules 直算 ─────────────────
//
// 用例取自 rules 包 golden_test.go 的 29 手金标（同手牌/同场况），
// 断言「引擎事件 payload 的 han/fu/points/score_deltas」与「直接用 rules
// Decompose→Evaluate→CountDora→Score 算出的结果」逐项相等。
// 这样 S5b 接入真实算点后，只要 rules 金标是对的，game 结算就是对的。

// scoringState 构造一个可控的计分局面：指定座位刚摸到 winning、轮到它行动。
// 有副露时 hand 必须是 14-3*len(melds) 张。
func scoringState(t *testing.T, seat, dealer Seat, hand []tile.Tile, melds []Meld, winning tile.Tile) *State {
	t.Helper()
	s := mustState(t, 42)
	s.Dealer = dealer
	s.Turn = seat
	s.Hands[seat] = append([]tile.Tile(nil), hand...)
	s.Melds[seat] = append([]Meld(nil), melds...)
	drawn := winning
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Scores = [4]int{25000, 25000, 25000, 25000}
	return s
}

// winEventPayload 取最后一条指定类型事件的 payload。
func winEventPayload(t *testing.T, s *State, kind EventKind) map[string]any {
	t.Helper()
	var found *Event
	for i := range s.Events {
		if s.Events[i].Kind == kind {
			found = &s.Events[i]
		}
	}
	if found == nil {
		t.Fatalf("没有找到 %v 事件，事件序列=%v", kind, eventKinds(s))
	}
	var payload map[string]any
	if err := json.Unmarshal(found.Payload, &payload); err != nil {
		t.Fatalf("解析 %v payload: %v", kind, err)
	}
	return payload
}

// payloadInt 从 JSON payload 里读整数（JSON 数字是 float64）。
func payloadInt(t *testing.T, payload map[string]any, key string) int {
	t.Helper()
	value, ok := payload[key]
	if !ok {
		t.Fatalf("payload 缺少字段 %q: %v", key, payload)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("payload[%q]=%v 不是数字", key, value)
	}
	return int(number)
}

// payloadDeltas 从 payload.score_deltas 读出四家点数变化。
func payloadDeltas(t *testing.T, payload map[string]any) [4]int {
	t.Helper()
	raw, ok := payload["score_deltas"].([]any)
	if !ok || len(raw) != 4 {
		t.Fatalf("payload.score_deltas 非法: %v", payload["score_deltas"])
	}
	var deltas [4]int
	for i, item := range raw {
		number, ok := item.(float64)
		if !ok {
			t.Fatalf("score_deltas[%d]=%v 不是数字", i, item)
		}
		deltas[i] = int(number)
	}
	return deltas
}

// rulesDirectDeltas 由 rules.Points 推导四家净点数变化（含本场；立直棒由引擎另算）。
func rulesDirectDeltas(points rules.Points, winner, dealer Seat, tsumo bool, ronTarget Seat, honba int) [4]int {
	var deltas [4]int
	if tsumo {
		for seat := 0; seat < 4; seat++ {
			if Seat(seat) == winner {
				continue
			}
			pay := points.TsumoChild
			if Seat(seat) == dealer {
				pay = points.TsumoDealer
			}
			pay += 100 * honba
			deltas[seat] -= pay
			deltas[winner] += pay
		}
		return deltas
	}
	deltas[ronTarget] -= points.Ron
	deltas[winner] += points.Ron
	return deltas
}

// TestRulesScorerGoldenTsumo 平和自摸（子，20 符 2 翻）：引擎结算 = rules 直算。
func TestRulesScorerGoldenTsumo(t *testing.T) {
	plainHand := "123m 456m 789p 11p 456s"
	hand := mustHand(t, plainHand)
	winning := mustTile(t, "6s")
	s := scoringState(t, 1, 0, hand, nil, winning)
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}

	// rules 直算
	shape, ok := rules.Decompose(hand, nil, winning)
	if !ok {
		t.Fatal("rules.Decompose 应判和")
	}
	ctx := rules.ScoreContext{Menzen: true, Tsumo: true, SeatWind: tile.South, RoundWind: tile.East,
		DoraIndicators: s.Wall.DoraIndicators()}
	_, yakuHan, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	ctx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, ctx)
	han := yakuHan + dora
	points := rules.Score(han, fu, ctx)

	payload := winEventPayload(t, s, EventTsumo)
	if got := payloadInt(t, payload, "han"); got != han {
		t.Fatalf("han=%d want %d（payload=%v）", got, han, payload)
	}
	if got := payloadInt(t, payload, "fu"); got != 20 {
		t.Fatalf("fu=%d want 20", got)
	}
	if got := payloadInt(t, payload, "points"); got != points.Total {
		t.Fatalf("points=%d want %d", got, points.Total)
	}
	want := rulesDirectDeltas(points, 1, 0, true, 0, 0)
	if got := payloadDeltas(t, payload); got != want {
		t.Fatalf("score_deltas=%v want %v", got, want)
	}
	var wantScores [4]int
	for seat := 0; seat < 4; seat++ {
		wantScores[seat] = 25000 + want[seat]
	}
	if s.Scores != wantScores {
		t.Fatalf("scores=%v want %v", s.Scores, wantScores)
	}
}

// TestRulesScorerGoldenChiitoitsuRon 七对子荣和（子，25 符 1600）：引擎结算 = rules 直算。
func TestRulesScorerGoldenChiitoitsuRon(t *testing.T) {
	hand13 := mustHand(t, "11m 22m 33m 44p 55p 66s 1z") // 门清 13 张（缺 1z）
	winning := mustTile(t, "1z")

	// 构造「seat0 打 1z，seat1 荣和」的手动响应局面
	s := mustManualState(t, 42)
	s.Dealer = 0
	s.Turn = 0
	s.Hands[0] = mustHand(t, "1z1m4m7m1s4s7s2p5p8p9p2z3z9m") // 含 1z 可打
	drawn := mustTile(t, "1z")
	s.Hands[0][13] = drawn
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Hands[1] = hand13
	s.Phase = PhaseAwaitDiscard
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 0, Tile: winning}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActRon, Seat: 1, Tile: winning}); err != nil {
		t.Fatalf("荣和失败: %v", err)
	}
	if s.Phase == PhaseAwaitResponse {
		if err := s.ResolveTimeout(); err != nil {
			t.Fatalf("结算其余响应失败: %v", err)
		}
	}

	// rules 直算
	full := append(append([]tile.Tile{}, hand13...), winning)
	shape, ok := rules.Decompose(full, nil, winning)
	if !ok {
		t.Fatal("rules.Decompose 应判和")
	}
	ctx := rules.ScoreContext{Menzen: true, SeatWind: tile.South, RoundWind: tile.East}
	_, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	points := rules.Score(han, fu, ctx)
	if han != 2 || fu != 25 || points.Ron != 1600 {
		t.Fatalf("rules 直算 han/fu/ron = %d/%d/%d，期望 2/25/1600", han, fu, points.Ron)
	}

	payload := winEventPayload(t, s, EventRon)
	if got := payloadInt(t, payload, "han"); got != 2 {
		t.Fatalf("han=%d want 2", got)
	}
	if got := payloadInt(t, payload, "fu"); got != 25 {
		t.Fatalf("fu=%d want 25", got)
	}
	if got := payloadInt(t, payload, "points"); got != 1600 {
		t.Fatalf("points=%d want 1600", got)
	}
	want := rulesDirectDeltas(points, 1, 0, false, 0, 0)
	if got := payloadDeltas(t, payload); got != want {
		t.Fatalf("score_deltas=%v want %v", got, want)
	}
	if s.Scores[1] != 26600 || s.Scores[0] != 23400 {
		t.Fatalf("scores=%v，期望 seat0=23400 seat1=26600", s.Scores)
	}
}

// TestRulesScorerGoldenKokushi 国士无双荣和（子，32000）：引擎结算 = rules 直算。
func TestRulesScorerGoldenKokushi(t *testing.T) {
	hand13 := mustHand(t, "19m 19p 19s 1234567z") // 国士 13 面听
	winning := mustTile(t, "1m")

	s := mustManualState(t, 42)
	s.Dealer = 0
	s.Turn = 0
	s.Hands[0] = mustHand(t, "1m1m4m7m1s4s7s2p5p8p9p2z3z9m")
	drawn := mustTile(t, "1m")
	s.Hands[0][13] = drawn
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Hands[1] = hand13
	t.Logf("debug: canRon=%v hand13=%v winning=%v", s.canRon(1, winning), s.Hands[1], winning)
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 0, Tile: winning}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	t.Logf("debug after discard: phase=%v pending=%+v", s.Phase, s.Pending)
	if err := s.Apply(Action{Kind: ActRon, Seat: 1, Tile: winning}); err != nil {
		t.Fatalf("荣和失败: %v", err)
	}
	if s.Phase == PhaseAwaitResponse {
		if err := s.ResolveTimeout(); err != nil {
			t.Fatalf("结算其余响应失败: %v", err)
		}
	}

	full := append(append([]tile.Tile{}, hand13...), winning)
	shape, ok := rules.Decompose(full, nil, winning)
	if !ok {
		t.Fatal("rules.Decompose 应判和（国士）")
	}
	ctx := rules.ScoreContext{Menzen: true, SeatWind: tile.South, RoundWind: tile.East}
	_, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if han != 26 || fu != 25 {
		t.Fatalf("rules 直算 han/fu = %d/%d，期望 26/25（国士十三面双倍役满）", han, fu)
	}
	points := rules.Score(han, fu, ctx)
	if points.Ron != 64000 {
		t.Fatalf("rules 直算荣和=%d want 64000", points.Ron)
	}

	payload := winEventPayload(t, s, EventRon)
	if got := payloadInt(t, payload, "han"); got != 26 {
		t.Fatalf("han=%d want 26", got)
	}
	if got := payloadInt(t, payload, "points"); got != 64000 {
		t.Fatalf("points=%d want 64000", got)
	}
	want := rulesDirectDeltas(points, 1, 0, false, 0, 0)
	if got := payloadDeltas(t, payload); got != want {
		t.Fatalf("score_deltas=%v want %v", got, want)
	}
}

// TestRulesScorerGoldenAkaDora 平和+门清自摸+赤宝牌 1（子，700/1300）：
// 赤宝牌由 RulesScorer 自己统计（Evaluate 不含宝牌），结果仍要与 rules 直算一致。
func TestRulesScorerGoldenAkaDora(t *testing.T) {
	hand := mustHand(t, "123m 456m 789p 11p 456s")
	red5s := tile.Tile{Kind: tile.Sou5, Aka: true}
	for i := range hand {
		if hand[i].Kind == tile.Sou5 {
			hand[i] = red5s
			break
		}
	}
	winning := mustTile(t, "6s")
	s := scoringState(t, 1, 0, hand, nil, winning)
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}

	// rules 直算：Evaluate 不含宝牌 → 加 CountDora（含赤）
	shape, ok := rules.Decompose(hand, nil, winning)
	if !ok {
		t.Fatal("rules.Decompose 应判和")
	}
	ctx := rules.ScoreContext{Menzen: true, Tsumo: true, SeatWind: tile.South, RoundWind: tile.East}
	_, yakuHan, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	ctx.DoraIndicators = s.Wall.DoraIndicators()
	ctx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, ctx)
	if yakuHan+dora < 3 {
		t.Fatalf("规则直算翻数=%d（役 %d + 宝牌 %d），应至少 3（赤宝牌必计）", yakuHan+dora, yakuHan, dora)
	}
	points := rules.Score(yakuHan+dora, fu, ctx)

	payload := winEventPayload(t, s, EventTsumo)
	if got := payloadInt(t, payload, "han"); got != yakuHan+dora {
		t.Fatalf("han=%d want %d（赤宝牌/宝牌应计入）", got, yakuHan+dora)
	}
	if got := payloadInt(t, payload, "fu"); got != 20 {
		t.Fatalf("fu=%d want 20", got)
	}
	want := rulesDirectDeltas(points, 1, 0, true, 0, 0)
	if got := payloadDeltas(t, payload); got != want {
		t.Fatalf("score_deltas=%v want %v", got, want)
	}
}

// TestRulesScorerRiichiDoraIndicators 立直+平和+宝牌 3（满贯 8000）：
// 直接调用 RulesScorer（自定义宝牌指示牌），与 rules 直算逐项相等。
func TestRulesScorerRiichiDoraIndicators(t *testing.T) {
	hand := mustHand(t, "123m 456m 789p 11p 456s")
	winning := mustTile(t, "6s")
	indicators := []tile.Tile{mustTile(t, "5s"), mustTile(t, "9p")}

	winCtx := WinContext{
		Seat:           1,
		DealerSeat:     0,
		RonTarget:      0,
		Tsumo:          false,
		RoundWind:      East,
		SeatWind:       South,
		Hand:           hand,
		WinningTile:    winning,
		Riichi:         true,
		DoraIndicators: indicators,
	}
	result, err := (RulesScorer{}).ScoreWin(winCtx)
	if err != nil {
		t.Fatalf("RulesScorer: %v", err)
	}

	shape, ok := rules.Decompose(hand, nil, winning)
	if !ok {
		t.Fatal("rules.Decompose 应判和")
	}
	ctx := rules.ScoreContext{
		Menzen:         true,
		Riichi:         true,
		SeatWind:       tile.South,
		RoundWind:      tile.East,
		DoraIndicators: indicators,
	}
	_, yakuHan, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	ctx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, ctx)
	points := rules.Score(yakuHan+dora, fu, ctx)
	if yakuHan+dora != 5 || points.Ron != 8000 {
		t.Fatalf("rules 直算 han/ron = %d/%d，期望 5/8000", yakuHan+dora, points.Ron)
	}
	if result.Han != 5 || result.Fu != fu {
		t.Fatalf("RulesScorer han/fu = %d/%d，期望 5/%d", result.Han, result.Fu, fu)
	}
	if result.Deltas[1] != 8000 || result.Deltas[0] != -8000 {
		t.Fatalf("RulesScorer deltas=%v，期望 +8000/-8000", result.Deltas)
	}
}

// TestRulesScorerMeldedYakuhai 副露和牌：碰东（场风役牌）+ 门清部分完成。
//
// 这手用「摊平」算法会把碰当暗刻（符数错、门清判定错）；RulesScorer 直接构造
// WinShape（Open=true）后与手写 shape 的 rules 直算对齐，验证副露拆解正确。
func TestRulesScorerMeldedYakuhai(t *testing.T) {
	// 副露：碰东（明面）；门清 11 张：123m 456m 789m 11p，摸 1p 自摸（单骑）
	hand := mustHand(t, "123m456m789m11p")
	winning := mustTile(t, "1p")
	melds := []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
	}, From: 2}}

	s := scoringState(t, 1, 0, hand, melds, winning)
	if !s.canTsumo(1) {
		t.Fatal("有副露的这手牌应判和")
	}
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}

	// 手写 WinShape：碰东（Open）+ 3 组门清顺子 + 11p 将，单骑待ち
	shape := rules.WinShape{
		Jantou: tile.Tile{Kind: tile.Pin1},
		Melds: []rules.MeldShape{
			{Kind: rules.MeldPon, Tiles: []tile.Tile{
				{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
			}, Open: true},
			{Kind: rules.MeldChi, Tiles: []tile.Tile{{Kind: tile.Man1}, {Kind: tile.Man2}, {Kind: tile.Man3}}},
			{Kind: rules.MeldChi, Tiles: []tile.Tile{{Kind: tile.Man4}, {Kind: tile.Man5}, {Kind: tile.Man6}}},
			{Kind: rules.MeldChi, Tiles: []tile.Tile{{Kind: tile.Man7}, {Kind: tile.Man8}, {Kind: tile.Man9}}},
		},
		Wait:    rules.WaitTanki,
		Pattern: rules.PatternStandard,
	}
	ctx := rules.ScoreContext{
		Menzen:    false,
		Tsumo:     true,
		SeatWind:  tile.South,
		RoundWind: tile.East,
	}
	_, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate(手写 shape): %v", err)
	}
	// 副露：场风东役牌 1 翻 + 一气通贯（副露减 1 翻）= 1 翻 → 共 2 翻；符 30
	if han != 2 || fu != 30 {
		t.Fatalf("手写 shape 直算 han/fu = %d/%d，期望 2/30", han, fu)
	}
	points := rules.Score(han, fu, ctx)
	want := rulesDirectDeltas(points, 1, 0, true, 0, 0)

	payload := winEventPayload(t, s, EventTsumo)
	if got := payloadInt(t, payload, "han"); got != 2 {
		t.Fatalf("引擎 han=%d want 2（副露后无门清自摸，但有一气+场风）", got)
	}
	if got := payloadInt(t, payload, "fu"); got != 30 {
		t.Fatalf("引擎 fu=%d want 30（碰东=明刻符）", got)
	}
	if got := payloadDeltas(t, payload); got != want {
		t.Fatalf("引擎 deltas=%v want %v", got, want)
	}
}

// TestRulesScorerAnkan 暗杠和牌：暗杠不破门清、按暗杠 16 符计（摊平算法会算错）。
func TestRulesScorerAnkan(t *testing.T) {
	// 暗杠 5p；门清 11 张：123m 456m 789m 11p，摸 1p 自摸
	hand := mustHand(t, "123m456m789m11p")
	winning := mustTile(t, "1p")
	melds := []Meld{{Kind: MeldKanAnkan, Tiles: []tile.Tile{
		{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
	}, From: 1}}

	s := scoringState(t, 1, 0, hand, melds, winning)
	if !s.canTsumo(1) {
		t.Fatal("暗杠后的和牌应判和")
	}
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 1}); err != nil {
		t.Fatalf("自摸失败: %v", err)
	}

	// 手写 shape：暗杠 5p（Open=false）+ 3 组顺子 + 11p 将，单骑
	shape := rules.WinShape{
		Jantou: tile.Tile{Kind: tile.Pin1},
		Melds: []rules.MeldShape{
			{Kind: rules.MeldKanAnkan, Tiles: []tile.Tile{
				{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
			}, Open: false},
			{Kind: rules.MeldChi, Tiles: []tile.Tile{{Kind: tile.Man1}, {Kind: tile.Man2}, {Kind: tile.Man3}}},
			{Kind: rules.MeldChi, Tiles: []tile.Tile{{Kind: tile.Man4}, {Kind: tile.Man5}, {Kind: tile.Man6}}},
			{Kind: rules.MeldChi, Tiles: []tile.Tile{{Kind: tile.Man7}, {Kind: tile.Man8}, {Kind: tile.Man9}}},
		},
		Wait:    rules.WaitTanki,
		Pattern: rules.PatternStandard,
	}
	ctx := rules.ScoreContext{
		Menzen:    true, // 暗杠不破门清
		Tsumo:     true,
		SeatWind:  tile.South,
		RoundWind: tile.East,
	}
	_, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate(手写 shape): %v", err)
	}
	// 门清自摸 1 翻 + 一气通贯 2 翻 = 3 翻；符 = 20 + 2(自摸) + 2(单骑) + 16(暗杠) = 40
	if han != 3 || fu != 40 {
		t.Fatalf("手写 shape 直算 han/fu = %d/%d，期望 3/40", han, fu)
	}
	points := rules.Score(han, fu, ctx)
	want := rulesDirectDeltas(points, 1, 0, true, 0, 0)

	payload := winEventPayload(t, s, EventTsumo)
	if got := payloadInt(t, payload, "fu"); got != 40 {
		t.Fatalf("引擎 fu=%d want 40（暗杠 16 符）", got)
	}
	if got := payloadInt(t, payload, "han"); got != 3 {
		t.Fatalf("引擎 han=%d want 3（暗杠仍门清自摸 + 一气）", got)
	}
	if got := payloadDeltas(t, payload); got != want {
		t.Fatalf("引擎 deltas=%v want %v", got, want)
	}
}
