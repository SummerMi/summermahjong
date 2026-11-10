package game

import (
	"encoding/json"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestStringers 覆盖各枚举的 String，便于日志/协议排错。
func TestStringers(t *testing.T) {
	for seat := Seat(0); seat < 4; seat++ {
		if got := seat.String(); got == "" || got[0] != 's' {
			t.Fatalf("Seat(%d).String()=%q", seat, got)
		}
	}
	winds := map[Wind]string{East: "东", South: "南", West: "西", North: "北", Wind(9): "?"}
	for wind, want := range winds {
		if got := wind.String(); got != want {
			t.Fatalf("Wind(%d).String()=%q want %q", wind, got, want)
		}
	}
	kinds := map[ActionKind]string{
		ActDraw: "draw", ActDiscard: "discard", ActChi: "chi", ActPon: "pon",
		ActKanDaiminkan: "kan_daiminkan", ActKanShouminkan: "kan_shouminkan",
		ActKanAnkan: "kan_ankan", ActRiichi: "riichi", ActTsumo: "tsumo",
		ActRon: "ron", ActKyuushu: "kyuushu", ActPass: "pass", ActionKind(99): "?",
	}
	for kind, want := range kinds {
		if got := kind.String(); got != want {
			t.Fatalf("ActionKind(%d).String()=%q want %q", kind, got, want)
		}
	}
	pendingKinds := map[PendingKind]string{
		ResponseToDiscard: "discard", ResponseToShouminkan: "shouminkan", PendingKind(9): "unknown",
	}
	for kind, want := range pendingKinds {
		if got := kind.String(); got != want {
			t.Fatalf("PendingKind(%d).String()=%q want %q", kind, got, want)
		}
	}
}

// TestBasicHelpers 覆盖 CurrentSeat / NextSeat / GameOver / CurrentWind。
func TestBasicHelpers(t *testing.T) {
	s := mustState(t, 1)
	if s.CurrentSeat() != s.Turn {
		t.Fatal("CurrentSeat 应等于 Turn")
	}
	if NextSeat(3) != 0 || NextSeat(0) != 1 {
		t.Fatal("NextSeat 环绕错误")
	}
	if s.GameOver() {
		t.Fatal("初始不应是整场结束")
	}
	s.Phase = PhaseGameEnd
	if !s.GameOver() {
		t.Fatal("game_end 时 GameOver 应为 true")
	}
	// 庄家自风为东，下家为北
	if s.CurrentWind(s.Dealer) != East {
		t.Fatalf("庄家自风=%v want 东", s.CurrentWind(s.Dealer))
	}
	if s.CurrentWind(NextSeat(s.Dealer)) != South {
		t.Fatalf("庄家下家自风=%v want 南", s.CurrentWind(NextSeat(s.Dealer)))
	}
}

// TestWinShapeWithMelds 有副露时的和牌判定（把副露摊回 14 张牌形再调用 rules）。
func TestWinShapeWithMelds(t *testing.T) {
	s := mustState(t, 42)
	s.Melds[0] = []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
	}, From: 1}}
	setActor(t, s, 0, "123m456m789m11p") // 3 组顺子 + 11p 将 + 副露 1 组 = 4 面子 + 将
	if !s.canTsumo(0) {
		t.Fatal("有副露的和牌应判和")
	}
	if err := s.Apply(Action{Kind: ActTsumo, Seat: 0}); err != nil {
		t.Fatalf("有副露自摸失败: %v", err)
	}
	if countEvents(s, EventTsumo) != 1 {
		t.Fatalf("tsumo 事件数=%d want 1", countEvents(s, EventTsumo))
	}
	// 七对子在有副露时不成立
	s2 := mustState(t, 42)
	s2.Melds[1] = []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
	}, From: 0}}
	hand := mustHand(t, "1m1m2m2m3m3m4m4m5p5p1z")
	if _, ok := winShape(hand, s2.Melds[1], mustTile(t, "1z")); ok {
		t.Fatal("有副露时七对不应成立")
	}
}

// TestLegalActionsIncludeSpecials 覆盖 Legal 里的立直/自摸/暗杠/加杠/九种九牌分支。
func TestLegalActionsIncludeSpecials(t *testing.T) {
	hasKind := func(actions []Action, kind ActionKind) bool {
		for _, action := range actions {
			if action.Kind == kind {
				return true
			}
		}
		return false
	}

	// 立直
	s := mustState(t, 42)
	setActor(t, s, 1, "123m456m789m11p34p2p")
	if !hasKind(s.Legal(), ActRiichi) {
		t.Fatalf("听牌时 Legal 应包含立直: %v", s.Legal())
	}
	// 暗杠
	s = mustState(t, 42)
	setActor(t, s, 0, "1z1z1z1z123m456m78m9p9p")
	if !hasKind(s.Legal(), ActKanAnkan) {
		t.Fatalf("4 张同种时 Legal 应包含暗杠: %v", s.Legal())
	}
	// 加杠
	s = mustState(t, 42)
	s.Melds[0] = []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
	}, From: 1}}
	setActor(t, s, 0, "123m456m789m1z5p")
	if !hasKind(s.Legal(), ActKanShouminkan) {
		t.Fatalf("有碰且有第 4 张时 Legal 应包含加杠: %v", s.Legal())
	}
	// 自摸
	s = mustState(t, 42)
	setActor(t, s, 0, "123m456m789m11p234p")
	if !hasKind(s.Legal(), ActTsumo) {
		t.Fatalf("和牌时 Legal 应包含自摸: %v", s.Legal())
	}
	// 九种九牌
	s = mustState(t, 7)
	s.Hands[0] = mustHand(t, "1m9m1p9p1s9s1z2z3z4z5z6z7z1m")
	drawn := s.Hands[0][13]
	s.Drawn = &drawn
	s.Phase = PhaseAwaitDiscard
	s.Turn = 0
	s.SeatDraws[0] = 1
	if !hasKind(s.Legal(), ActKyuushu) {
		t.Fatalf("九种九牌时 Legal 应包含 kyuushu: %v", s.Legal())
	}
}

// TestAnkanShouminkanRejections 覆盖暗杠/加杠的拒绝分支。
func TestAnkanShouminkanRejections(t *testing.T) {
	// 立直后不可暗杠
	s := mustState(t, 42)
	s.Riichi[0] = true
	setActor(t, s, 0, "1z1z1z1z123m456m78m9p9p")
	if err := s.Apply(Action{Kind: ActKanAnkan, Seat: 0, Tile: mustTile(t, "1z")}); err == nil {
		t.Fatal("立直后暗杠应失败（简化规则）")
	}

	// 加杠：没有碰
	s = mustState(t, 42)
	setActor(t, s, 0, "123m456m789m1z5p1p2p3p")
	if err := s.Apply(Action{Kind: ActKanShouminkan, Seat: 0, Tile: mustTile(t, "5p")}); err == nil {
		t.Fatal("没有碰时加杠应失败")
	}

	// 加杠：有碰但手里没有第 4 张
	s = mustState(t, 42)
	s.Melds[0] = []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
	}, From: 1}}
	setActor(t, s, 0, "123m456m789m1z9p")
	if err := s.Apply(Action{Kind: ActKanShouminkan, Seat: 0, Tile: mustTile(t, "5p")}); err == nil {
		t.Fatal("手里没有第 4 张时加杠应失败")
	}
}

// TestBasicPointsLimits 覆盖满贯以上封顶与切上取整。
func TestBasicPointsLimits(t *testing.T) {
	cases := []struct {
		han, fu, want int
	}{
		{1, 30, 240},
		{3, 30, 960},
		{5, 30, 2000},
		{6, 30, 3000},
		{8, 30, 4000},
		{11, 30, 6000},
		{13, 30, 8000},
		{1, 20, 160},
	}
	for _, tc := range cases {
		if got := basicPoints(tc.han, tc.fu); got != tc.want {
			t.Fatalf("basicPoints(%d,%d)=%d want %d", tc.han, tc.fu, got, tc.want)
		}
	}
	if ceil100(0) != 0 || ceil100(-10) != 0 {
		t.Fatal("ceil100 非正数应返回 0")
	}
	if ceil100(101) != 200 || ceil100(100) != 100 {
		t.Fatal("ceil100 切上错误")
	}
}

// TestDoubleRonWithRiichiSticks 双响 + 立直棒：立直棒归最近的和了者。
func TestDoubleRonWithRiichiSticks(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p",
		"123m456m789m11p34p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	s.RiichiStix = 1
	applyDiscard(t, s, 0, "5p")
	applyRon(t, s, 1, "5p")
	applyRon(t, s, 2, "5p")
	if s.Scores[1] != 27000 { // 1000 荣和 + 1000 立直棒
		t.Fatalf("seat1 scores=%d want 27000", s.Scores[1])
	}
	if s.Scores[2] != 26000 {
		t.Fatalf("seat2 scores=%d want 26000（立直棒只给最近一家）", s.Scores[2])
	}
}

// TestChiConsumedMismatch 吃的手牌组合与选项不一致 → 非法。
func TestChiConsumedMismatch(t *testing.T) {
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	applyDiscard(t, s, 0, "5p")
	// 用不存在的 Consumed 组合吃
	wrong := Action{Kind: ActChi, Seat: 1, Tile: mustTile(t, "5p"),
		Consumed: []tile.Tile{{Kind: tile.Sou1}, {Kind: tile.Sou2}}}
	if err := s.Apply(wrong); err == nil {
		t.Fatal("Consumed 与选项不符时吃应失败")
	}
	// pass 之后不能再响应
	applyResponse(t, s, 1, ActPass)
	if err := s.Apply(Action{Kind: ActPass, Seat: 1}); err == nil {
		t.Fatal("已响应的座位不能再次响应")
	}
}

// TestIsTenpaiEdgeCases 覆盖 isTenpai / canTsumo / hasPon / findHandTile 的否定分支。
func TestIsTenpaiEdgeCases(t *testing.T) {
	s := mustState(t, 42)
	s.Hands[0] = mustHand(t, "1m4m7m1s4s7s2p5p8p1z2z3z9s")
	if s.isTenpai(0) {
		t.Fatal("散牌 13 张不应判听牌")
	}
	if s.canTsumo(0) {
		t.Fatal("未摸牌时 canTsumo 应为 false")
	}
	if s.hasPon(0, tile.Pin5) {
		t.Fatal("没有副露时 hasPon 应为 false")
	}
	if s.findHandTile(0, tile.Tile{Kind: tile.Pin9, Aka: true}) >= 0 {
		t.Fatal("手里没有赤 9p，findHandTile 应返回 -1")
	}
}

// TestRiichiTwiceRejected 已经立直不能再立直。
func TestRiichiTwiceRejected(t *testing.T) {
	s := mustState(t, 42)
	setActor(t, s, 1, "123m456m789m11p34p2p")
	if err := s.Apply(Action{Kind: ActRiichi, Seat: 1, Tile: mustTile(t, "2p")}); err != nil {
		t.Fatalf("首次立直失败: %v", err)
	}
	// 立直后回到自己摸牌，再尝试立直
	if err := s.Apply(Action{Kind: ActDraw, Seat: 2}); err != nil {
		t.Fatalf("摸牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 2, Tile: *s.Drawn}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDraw, Seat: 3}); err != nil {
		t.Fatalf("摸牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 3, Tile: *s.Drawn}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDraw, Seat: 0}); err != nil {
		t.Fatalf("摸牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 0, Tile: *s.Drawn}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActDraw, Seat: 1}); err != nil {
		t.Fatalf("摸牌失败: %v", err)
	}
	if err := s.Apply(Action{Kind: ActRiichi, Seat: 1, Tile: *s.Drawn}); err == nil {
		t.Fatal("已立直时再次立直应失败")
	}
}

// TestSouthWindRollover 东 4 局结束后进入南 1 局。
func TestSouthWindRollover(t *testing.T) {
	s := mustState(t, 42)
	s.RoundWind = East
	s.Kyoku = 4
	s.Phase = PhaseRoundEnd
	s.LastDealerRepeat = false
	if err := s.StartNextRound(); err != nil {
		t.Fatalf("StartNextRound: %v", err)
	}
	if s.RoundWind != South || s.Kyoku != 1 {
		t.Fatalf("进入 %v%d 局，期望 南 1", s.RoundWind, s.Kyoku)
	}
}

// TestReplayWithOptionsError 非法动作序列回放应报错。
func TestReplayWithOptionsError(t *testing.T) {
	// 默认（自动 pass）模式回放「手动响应动作」会因阶段不符报错
	actions := []Action{{Kind: ActPass, Seat: 1}}
	if _, err := ReplayWithOptions(7, DefaultRuleset(), actions, Options{}); err == nil {
		t.Fatal("阶段不符的回放应报错")
	}
}

// TestObserveMeldsClone 有副露时 Observation 会深拷贝副露（防别名）。
func TestObserveMeldsClone(t *testing.T) {
	s := mustState(t, 42)
	s.Melds[2] = []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
	}, From: 0}}
	obs := s.Observe(2)
	if len(obs.Melds[2]) != 1 || len(obs.Melds[2][0].Tiles) != 3 {
		t.Fatalf("观察到的副露=%v", obs.Melds[2])
	}
	obs.Melds[2][0].Tiles[0] = tile.Tile{Kind: tile.Pin9}
	if s.Melds[2][0].Tiles[0].Kind != tile.East {
		t.Fatal("修改观察结果不应污染引擎副露")
	}
}

// TestRyuukyokuAllTenpai 四家全听牌时不产生听牌料（总和 0）。
func TestRyuukyokuAllTenpai(t *testing.T) {
	s := mustState(t, 42)
	// 构造四家都听牌的局面（手牌 13 张听牌形）
	tenpai := mustHand(t, "123m456m789m11p34p")
	for seat := 0; seat < 4; seat++ {
		s.Hands[seat] = append([]tile.Tile(nil), tenpai...)
	}
	s.Phase = PhaseAwaitDraw
	s.finishRyuukyoku(RyuukyokuHaitei, false)
	if s.Scores != [4]int{25000, 25000, 25000, 25000} {
		t.Fatalf("四家全听牌不应有听牌料: %v", s.Scores)
	}
	var payload struct {
		TenpaiSeats []int  `json:"tenpai_seats"`
		ScoreDeltas [4]int `json:"score_deltas"`
	}
	for _, ev := range s.Events {
		if ev.Kind == EventRyuukyoku {
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatalf("解析 payload: %v", err)
			}
		}
	}
	if len(payload.TenpaiSeats) != 4 {
		t.Fatalf("听牌座位数=%d want 4", len(payload.TenpaiSeats))
	}
}
