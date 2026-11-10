package agent

import (
	"testing"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// mustParse 在测试里解析简写手牌（"123m456p..."）。
func mustParse(t *testing.T, text string) []tile.Tile {
	t.Helper()
	hand, err := tile.ParseHand(text)
	if err != nil {
		t.Fatalf("tile.ParseHand(%q): %v", text, err)
	}
	return hand
}

// checkShantenConsistency 校验 14 张形态的 shantenAny 与「枚举切牌后 rules.Shanten」一致。
//
// 唯一例外：和牌形（shantenAny = -1）下，「先切一张」必然把和牌拆成听牌，
// 所以 min-over-discard 是 0 而不是 -1；两者都算合法。
func checkShantenConsistency(t *testing.T, label string, hand []tile.Tile, got, want int) {
	t.Helper()
	if got == -1 && want == 0 {
		return
	}
	if got != want {
		t.Fatalf("%s: hand=%v shantenAny=%d want(min over discards via rules.Shanten)=%d", label, hand, got, want)
	}
}

// TestShantenAnyMatchesRulesAfterDiscard 是内部正确性的关键对拍：
// shantenAny 对 14 张形态的结果，必须等于「枚举所有切牌后 rules.Shanten 的最小值」。
// 这条测试把 agent 自己实现的 DFS 与 S3 的权威实现锁在一起。
func TestShantenAnyMatchesRulesAfterDiscard(t *testing.T) {
	deck, err := tile.BuildDeck(0)
	if err != nil {
		t.Fatalf("tile.BuildDeck: %v", err)
	}

	// 门清 14 张。
	for trial := 0; trial < 300; trial++ {
		tile.Shuffle(deck, int64(trial+1))
		hand := append([]tile.Tile(nil), deck[:14]...)
		got := shantenAny(countsOf(hand), 0)

		want := 99
		seen := make(map[tile.Tile]bool, 14)
		for _, d := range hand {
			if seen[d] {
				continue
			}
			seen[d] = true
			after, ok := removeTile(hand, d)
			if !ok {
				t.Fatalf("trial %d: removeTile 失败", trial)
			}
			if s := rules.Shanten(after, 0); s < want {
				want = s
			}
		}
		checkShantenConsistency(t, "trial "+itoa(trial), hand, got, want)
	}

	// 副露 1 组：手牌 11 张（14-3），切后 10 张 + 3 = 13。
	for trial := 0; trial < 200; trial++ {
		tile.Shuffle(deck, int64(trial+1001))
		hand := append([]tile.Tile(nil), deck[:11]...)
		got := shantenAny(countsOf(hand), 1)

		want := 99
		seen := make(map[tile.Tile]bool, 11)
		for _, d := range hand {
			if seen[d] {
				continue
			}
			seen[d] = true
			after, ok := removeTile(hand, d)
			if !ok {
				t.Fatalf("trial %d: removeTile 失败", trial)
			}
			if s := rules.Shanten(after, 1); s < want {
				want = s
			}
		}
		checkShantenConsistency(t, "meld trial "+itoa(trial), hand, got, want)
	}
}

// TestShantenAnyChiitoiStrict 覆盖七对的日麻严格语义（4 张相同只算一对），
// 并覆盖标准型与七对同时成立、和牌形 -1 等边界。
func TestShantenAnyChiitoiStrict(t *testing.T) {
	// 13 张形态：直接与 rules.Shanten 对拍。
	tenpaiShapes := []string{
		"1111m22p33p44s55s1z", // 13：4 张相同只算一对 → 不是七对听
	}
	for _, text := range tenpaiShapes {
		hand := mustParse(t, text)
		if len(hand) != 13 {
			t.Fatalf("%s 应为 13 张，实际 %d", text, len(hand))
		}
		got := shantenAny(countsOf(hand), 0)
		want := rules.Shanten(hand, 0)
		if got != want {
			t.Fatalf("%s: shantenAny(13)=%d want rules.Shanten=%d", text, got, want)
		}
	}

	// 14 张形态：与「枚举切牌后 rules.Shanten」对拍（和牌形例外见 checkShantenConsistency）。
	winShapes := []string{
		"1111m22p33p44s55s11z", // 14：4 张相同只算一对 → 不是七对
		"1111m2222p33s44s11z",  // 14：两组四张 + 三对
		"11223344556677m",      // 14：7 种不同对子 → 和牌形
	}
	for _, text := range winShapes {
		hand := mustParse(t, text)
		if len(hand) != 14 {
			t.Fatalf("%s 应为 14 张，实际 %d", text, len(hand))
		}
		got := shantenAny(countsOf(hand), 0)
		want := 99
		seen := make(map[tile.Tile]bool, len(hand))
		for _, d := range hand {
			if seen[d] {
				continue
			}
			seen[d] = true
			after, ok := removeTile(hand, d)
			if !ok {
				t.Fatalf("%s: removeTile 失败", text)
			}
			if s := rules.Shanten(after, 0); s < want {
				want = s
			}
		}
		checkShantenConsistency(t, text, hand, got, want)
	}

	// 七对严格语义下，4 张相同的那手不是和牌形。
	notWin := mustParse(t, "1111m22p33p44s55s11z")
	if got := shantenAny(countsOf(notWin), 0); got == -1 {
		t.Fatalf("日麻严格七对下 %s 不应是和牌形（shantenAny=-1）", notWin)
	}
	win := mustParse(t, "11223344556677m")
	if got := shantenAny(countsOf(win), 0); got != -1 {
		t.Fatalf("7 种不同对子应为和牌形，shantenAny=%d", got)
	}
}

// itoa 避免为测试引入 strconv 依赖。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestShantenAnyCompleteAndTenpai 覆盖 -1（和牌形）与 0（听牌）。
func TestShantenAnyCompleteAndTenpai(t *testing.T) {
	win := mustParse(t, "123m456p789s123p99s")
	if len(win) != 14 {
		t.Fatalf("测试手牌应为 14 张，实际 %d", len(win))
	}
	if got := shantenAny(countsOf(win), 0); got != -1 {
		t.Fatalf("和牌形 shantenAny=%d want -1", got)
	}
	tenpai := mustParse(t, "123m456p789s11p45s")
	if len(tenpai) != 13 {
		t.Fatalf("测试手牌应为 13 张，实际 %d", len(tenpai))
	}
	if got := shantenAny(countsOf(tenpai), 0); got != 0 {
		t.Fatalf("听牌形 shantenAny=%d want 0", got)
	}
	if got := shantenAny(countsOf(win), -1); got != shantenInvalid {
		t.Fatalf("非法 meldCount 应返回 %d，实际 %d", shantenInvalid, got)
	}
	if got := shantenAny(countsOf(win), meldsRequired+1); got != shantenInvalid {
		t.Fatalf("非法 meldCount 应返回 %d，实际 %d", shantenInvalid, got)
	}
}

// TestUkeireTwoSided 覆盖两面听的有效进张统计（3s、6s 各 4 张 = 8）。
func TestUkeireTwoSided(t *testing.T) {
	hand := mustParse(t, "123m456p789s11p45s")
	base := rules.Shanten(hand, 0)
	if base != 0 {
		t.Fatalf("测试手牌应听牌，shanten=%d", base)
	}
	visible := countsOf(hand) // 只把自己的手牌视为可见
	if got := ukeire(hand, 0, base, visible); got != 8 {
		t.Fatalf("ukeire=%d want 8（3s×4 + 6s×4）", got)
	}
	if got := ukeire(hand, 0, shantenInvalid, visible); got != 0 {
		t.Fatalf("非法向听应返回 0，实际 %d", got)
	}
}

// TestVisibleCountsAndRemaining 覆盖「可见牌 → 剩余张数」的换算。
func TestVisibleCountsAndRemaining(t *testing.T) {
	obs := game.Observation{
		Hand: mustParse(t, "1m2m"),
		Discards: [4][]tile.Tile{
			1: {{Kind: tile.Man1}},
		},
		DoraIndicators: []tile.Tile{{Kind: tile.Man1}},
	}
	visible := visibleCounts(obs)
	if got := int(visible[tile.Man1]); got != 3 {
		t.Fatalf("1m 可见张数=%d want 3（手牌 1 + 舍牌 1 + 宝牌指示牌 1）", got)
	}
	if got := remainingCopies(visible, tile.Man1); got != 1 {
		t.Fatalf("1m 剩余=%d want 1", got)
	}
	if got := remainingCopies(visible, tile.Man2); got != 3 {
		t.Fatalf("2m 剩余=%d want 3", got)
	}
	if got := remainingCopies(visible, tile.Kind(tile.NumKinds)); got != 0 {
		t.Fatalf("越界牌种剩余=%d want 0", got)
	}
	// 超过 4 张可见也不能返回负数。
	visible[tile.Man1] = 9
	if got := remainingCopies(visible, tile.Man1); got != 0 {
		t.Fatalf("超量可见时剩余=%d want 0", got)
	}
}

// TestCandidateDiscards 覆盖候选切牌去重与 M2 动作过滤。
func TestCandidateDiscards(t *testing.T) {
	obs := game.Observation{
		LegalActions: []game.Action{
			{Kind: game.ActDiscard, Tile: tile.Tile{Kind: tile.Man1}},
			{Kind: game.ActDiscard, Tile: tile.Tile{Kind: tile.Man1}}, // 同牌面重复：去重
			{Kind: game.ActRiichi},
			{Kind: game.ActDiscard, Tile: tile.Tile{Kind: tile.Man2}},
		},
	}
	cands := candidateDiscards(obs)
	if len(cands) != 2 || cands[0].Kind != tile.Man1 || cands[1].Kind != tile.Man2 {
		t.Fatalf("candidateDiscards=%v want [1m 2m]", cands)
	}
	if got := candidateDiscards(game.Observation{}); len(got) != 0 {
		t.Fatalf("空合法集应返回空候选，实际 %v", got)
	}
}

// TestRemoveTile 覆盖去牌（不修改入参、找不到时返回 false）。
func TestRemoveTile(t *testing.T) {
	hand := mustParse(t, "1m2m3m")
	after, ok := removeTile(hand, tile.Tile{Kind: tile.Man2})
	if !ok {
		t.Fatal("removeTile 应成功")
	}
	if len(after) != 2 || after[0].Kind != tile.Man1 || after[1].Kind != tile.Man3 {
		t.Fatalf("removeTile 结果=%v", after)
	}
	if len(hand) != 3 {
		t.Fatalf("removeTile 不得修改入参，len=%d", len(hand))
	}
	if _, ok := removeTile(hand, tile.Tile{Kind: tile.Man9}); ok {
		t.Fatal("不存在的牌应返回 false")
	}
}

// TestFallbackActionPriority 覆盖「和了 > 摸牌 > 第一个合法动作」的优先级。
func TestFallbackActionPriority(t *testing.T) {
	obs := game.Observation{LegalActions: []game.Action{
		{Kind: game.ActDiscard},
		{Kind: game.ActDraw},
		{Kind: game.ActTsumo},
	}}
	action, err := fallbackAction(obs)
	if err != nil {
		t.Fatalf("fallbackAction: %v", err)
	}
	if action.Kind != game.ActTsumo {
		t.Fatalf("优先级最高的动作=%v want tsumo", action.Kind)
	}

	drawOnly := game.Observation{LegalActions: []game.Action{
		{Kind: game.ActPass},
		{Kind: game.ActDraw},
	}}
	if action, _ := fallbackAction(drawOnly); action.Kind != game.ActDraw {
		t.Fatalf("摸牌应优先于 pass，实际 %v", action.Kind)
	}

	if _, err := fallbackAction(game.Observation{}); err != ErrNoLegalAction {
		t.Fatalf("空合法集应返回 ErrNoLegalAction，实际 %v", err)
	}
}

// TestDangerNoRiichi 验证无人立直时危险度为 0（不凭空制造风险）。
func TestDangerNoRiichi(t *testing.T) {
	obs := game.Observation{Seat: 0, Hand: mustParse(t, "4m")}
	if got := dangerScore(obs, tile.Tile{Kind: tile.Man4}, visibleCounts(obs)); got != 0 {
		t.Fatalf("无立直时危险度=%v want 0", got)
	}
}

// TestDangerGenbutsu 验证现物危险度为 0。
func TestDangerGenbutsu(t *testing.T) {
	obs := game.Observation{
		Seat:   0,
		Hand:   mustParse(t, "4m5m"),
		Riichi: [4]bool{1: true},
		Discards: [4][]tile.Tile{
			1: {{Kind: tile.Man4}},
		},
	}
	if got := dangerScore(obs, tile.Tile{Kind: tile.Man4}, visibleCounts(obs)); got != 0 {
		t.Fatalf("现物 4m 危险度=%v want 0", got)
	}
	if got := dangerScore(obs, tile.Tile{Kind: tile.Man5}, visibleCounts(obs)); got <= 0 {
		t.Fatalf("非现物 5m 危险度应 > 0，实际 %v", got)
	}
}

// TestDangerSujiAndWall 验证筋与壁会降低危险度。
func TestDangerSujiAndWall(t *testing.T) {
	base := game.Observation{
		Seat:   0,
		Hand:   mustParse(t, "2m4m5m"),
		Riichi: [4]bool{1: true},
	}
	visibleBase := visibleCounts(base)
	sujiObs := base
	sujiObs.Discards[1] = []tile.Tile{{Kind: tile.Man1}} // 1m 打过 → 4m 是筋
	plain := dangerScore(base, tile.Tile{Kind: tile.Man4}, visibleBase)
	suji := dangerScore(sujiObs, tile.Tile{Kind: tile.Man4}, visibleCounts(sujiObs))
	if suji >= plain {
		t.Fatalf("筋牌 4m 危险度=%v 应低于无筋 %v", suji, plain)
	}

	// 壁：2m 四张全部可见（手牌 1 + 舍牌 3）→ 4m 的 -2 侧被壁挡住。
	wallObs := base
	wallObs.Discards[1] = []tile.Tile{
		{Kind: tile.Man2}, {Kind: tile.Man2}, {Kind: tile.Man2},
	}
	wall := dangerScore(wallObs, tile.Tile{Kind: tile.Man4}, visibleCounts(wallObs))
	if wall >= plain {
		t.Fatalf("壁内 4m 危险度=%v 应低于无壁 %v", wall, plain)
	}
}

// TestDangerHonorRemaining 验证字牌危险度随可见张数增加而下降。
func TestDangerHonorRemaining(t *testing.T) {
	manyVisible := game.Observation{
		Seat:   0,
		Hand:   mustParse(t, "555z6z"), // 3 张白 + 1 张发（5z=白，6z=发）
		Riichi: [4]bool{1: true},
	}
	fewVisible := game.Observation{
		Seat:   0,
		Hand:   mustParse(t, "5z6z"),
		Riichi: [4]bool{1: true},
	}
	many := dangerScore(manyVisible, tile.Tile{Kind: tile.Haku}, visibleCounts(manyVisible))
	few := dangerScore(fewVisible, tile.Tile{Kind: tile.Haku}, visibleCounts(fewVisible))
	if many >= few {
		t.Fatalf("字牌可见 3 张危险度=%v 应低于可见 1 张 %v", many, few)
	}
}

// TestKeepValue 覆盖牌价值启发式与断幺倾向。
func TestKeepValue(t *testing.T) {
	attackMiddle := keepValue(tile.Man5, StyleAttack)
	attackTerminal := keepValue(tile.Man1, StyleAttack)
	attackHonor := keepValue(tile.Haku, StyleAttack)
	if attackMiddle <= attackTerminal {
		t.Fatalf("速攻下中张保留价值（%v）应高于幺九（%v）", attackMiddle, attackTerminal)
	}
	if attackMiddle <= attackHonor {
		t.Fatalf("速攻下中张保留价值（%v）应高于字牌（%v）", attackMiddle, attackHonor)
	}
	if got := keepValue(tile.Man5, StyleDefense); got != 5 {
		t.Fatalf("守備下中张保留价值=%v want 5（TanyaoBias=0）", got)
	}
	if got := keepValue(tile.Kind(tile.NumKinds), StyleAttack); got < 0 || got > maxKeepValue {
		t.Fatalf("越界牌价值=%v 不在 0..%v", got, maxKeepValue)
	}
}

// TestStylePresets 校验三种打风的权重关系与查表。
func TestStylePresets(t *testing.T) {
	if StyleDefense.DangerWeight <= StyleClosed.DangerWeight || StyleClosed.DangerWeight <= StyleAttack.DangerWeight {
		t.Fatalf("危险度权重应满足 attack < closed < defense: %v %v %v",
			StyleAttack.DangerWeight, StyleClosed.DangerWeight, StyleDefense.DangerWeight)
	}
	if StyleAttack.MeldsBias <= StyleClosed.MeldsBias {
		t.Fatalf("副露倾向应满足 attack > closed: %v %v", StyleAttack.MeldsBias, StyleClosed.MeldsBias)
	}
	if StyleClosed.RiichiBias <= StyleAttack.RiichiBias {
		t.Fatalf("立直倾向应满足 closed > attack: %v %v", StyleClosed.RiichiBias, StyleAttack.RiichiBias)
	}
	for _, name := range []string{"attack", "closed", "defense"} {
		got, ok := StyleByName(name)
		if !ok {
			t.Fatalf("StyleByName(%q) 应命中", name)
		}
		if got.Name != name {
			t.Fatalf("StyleByName(%q).Name=%q", name, got.Name)
		}
	}
	if _, ok := StyleByName("unknown"); ok {
		t.Fatal("未知打风不应命中")
	}
}

// TestNormalizeStyle 覆盖零值 Style 的兜底。
func TestNormalizeStyle(t *testing.T) {
	if got := normalizeStyle(Style{}, StyleDefense); got.Name != StyleDefense.Name {
		t.Fatalf("零值 Style 应回退到默认打风，实际 %q", got.Name)
	}
	if got := normalizeStyle(StyleAttack, StyleDefense); got.Name != StyleAttack.Name {
		t.Fatalf("显式 Style 不应被默认值覆盖，实际 %q", got.Name)
	}
}

// TestInterestingDrawKinds 固定「可能改变向听」的牌种集合：
// 同种 + 同花色 ±1/±2 + 全部幺九字牌（国士路线）；字牌只有同种。
func TestInterestingDrawKinds(t *testing.T) {
	c := countsOf(mustParse(t, "1m5s9p1z"))
	got := interestingDrawKinds(c, 0)
	want := map[int]bool{
		// 手牌邻接 / 同种
		int(tile.Man1): true, int(tile.Man2): true, int(tile.Man3): true,
		int(tile.Pin7): true, int(tile.Pin8): true, int(tile.Pin9): true,
		int(tile.Sou3): true, int(tile.Sou4): true, int(tile.Sou5): true,
		int(tile.Sou6): true, int(tile.Sou7): true,
		// 全部幺九字牌（国士无双）
		int(tile.Man9): true,
		int(tile.Pin1): true,
		int(tile.Sou1): true, int(tile.Sou9): true,
		int(tile.East): true, int(tile.South): true, int(tile.West): true,
		int(tile.North): true, int(tile.Haku): true, int(tile.Hatsu): true, int(tile.Chun): true,
	}
	if len(got) != len(want) {
		t.Fatalf("interestingDrawKinds=%v，数量 %d，期望 %d", got, len(got), len(want))
	}
	seen := make(map[int]bool, len(got))
	for _, kind := range got {
		if !want[kind] {
			t.Fatalf("多出候选牌种 %v", tile.Kind(kind))
		}
		seen[kind] = true
	}
	for kind := range want {
		if !seen[kind] {
			t.Fatalf("缺少候选牌种 %v", tile.Kind(kind))
		}
	}
	// 结果必须升序（保证策略确定性）。
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("interestingDrawKinds 未升序: %v", got)
		}
	}

	// 有副露后国士不可能成立：不再把无关幺九字牌算作候选。
	withMelds := interestingDrawKinds(c, 1)
	for _, kind := range withMelds {
		if tile.Kind(kind) == tile.Man9 || tile.Kind(kind) == tile.Sou1 {
			t.Fatalf("有副露时不应把 %v 当作候选（国士不可能）", tile.Kind(kind))
		}
	}
	if len(withMelds) >= len(got) {
		t.Fatalf("有副露时候选应更少：%d vs %d", len(withMelds), len(got))
	}
}

// TestInterestingPruneIsSound 是剪枝正确性的对拍：
// 对随机 13 张手牌，凡是「不 interesting」的牌种，摸到后向听必须保持 base，
// 也就是剪掉它们不会漏掉有效进张（否则 analyzeAfter 的期望会被低估）。
func TestInterestingPruneIsSound(t *testing.T) {
	deck, err := tile.BuildDeck(0)
	if err != nil {
		t.Fatalf("tile.BuildDeck: %v", err)
	}
	for trial := 0; trial < 120; trial++ {
		tile.Shuffle(deck, int64(trial+1))
		hand := append([]tile.Tile(nil), deck[:13]...)
		base := rules.Shanten(hand, 0)
		if base == shantenInvalid {
			continue
		}
		c := countsOf(hand)
		interesting := make(map[int]bool, tile.NumKinds)
		for _, kind := range interestingDrawKinds(c, 0) {
			interesting[kind] = true
		}
		for kind := 0; kind < tile.NumKinds; kind++ {
			if interesting[kind] {
				continue
			}
			next := c
			next[kind]++
			if got := shantenAny(next, 0); got != base {
				t.Fatalf("trial %d hand=%v: 非候选牌 %v 把向听从 %d 改成了 %d（剪枝不安全）",
					trial, hand, tile.Kind(kind), base, got)
			}
		}
	}
}

// TestAnalyzeAfterMatchesBruteForce 用全量 34 种进张对拍 analyzeAfter 的剪枝实现。
func TestAnalyzeAfterMatchesBruteForce(t *testing.T) {
	hand := mustParse(t, "123m456p789s11p45s")
	base := rules.Shanten(hand, 0)
	visible := countsOf(hand)

	gotUkeire, gotExpected := analyzeAfter(hand, 0, base, visible)

	c := countsOf(hand)
	wantUkeire := 0
	totalWeight := 0
	weighted := 0.0
	for kind := 0; kind < tile.NumKinds; kind++ {
		rem := remainingCopies(visible, tile.Kind(kind))
		if rem == 0 {
			continue
		}
		next := c
		next[kind]++
		s := shantenAny(next, 0)
		totalWeight += rem
		weighted += float64(rem) * float64(s)
		if s < base {
			wantUkeire += rem
		}
	}
	wantExpected := weighted / float64(totalWeight)

	if gotUkeire != wantUkeire {
		t.Fatalf("analyzeAfter ukeire=%d want %d", gotUkeire, wantUkeire)
	}
	if diff := gotExpected - wantExpected; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("analyzeAfter expected=%v want %v", gotExpected, wantExpected)
	}
	if uk, exp := analyzeAfter(hand, 0, shantenInvalid, visible); uk != 0 || exp != 0 {
		t.Fatalf("非法向听应返回 (0,0)，实际 (%d,%v)", uk, exp)
	}
	// 全部牌都可见（剩余 0）时退化为当前向听。
	var allVisible counts34
	for kind := 0; kind < tile.NumKinds; kind++ {
		allVisible[kind] = tile.CopiesPerKind
	}
	if uk, exp := analyzeAfter(hand, 0, base, allVisible); uk != 0 || exp != float64(base) {
		t.Fatalf("无剩余牌应返回 (0,base)，实际 (%d,%v)", uk, exp)
	}
}
