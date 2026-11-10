package rules

import (
	"slices"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestDecomposeStandardWithWaitType 覆盖标准型的各种待ち类型：两面 / 嵌张 / 边张 / 双碰 / 单骑。
func TestDecomposeStandardWithWaitType(t *testing.T) {
	cases := []struct {
		name        string
		hand        string
		winning     string
		wantWait    WaitType
		wantPattern string
	}{
		{"单骑将", "123m 456m 789m 123p 99s", "9s", WaitTanki, PatternStandard},
		{"刻子双碰", "111m 222m 333m 555p 66p", "5p", WaitShanpon, PatternStandard},
		{"四连刻回溯拆成顺子", "111222333444m 56m", "6m", WaitRyanmen, PatternStandard},
		{"两面", "123m 456m 789m 11p 456s", "6s", WaitRyanmen, PatternStandard},
		{"嵌张", "123m 456m 789m 11p 456s", "5s", WaitKanchan, PatternStandard},
		{"边张一二三", "123m 456m 789m 11p 123s", "3s", WaitPenchan, PatternStandard},
		{"边张七八九", "123m 456m 789m 11p 789s", "7s", WaitPenchan, PatternStandard},
		{"双碰", "123m 456m 789m 111p 22p", "1p", WaitShanpon, PatternStandard},
		{"字牌刻子带数牌将", "111z 222z 333z 444z 11m", "1m", WaitTanki, PatternStandard},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			winning := tileFromName(t, tc.winning)
			shape, ok := Decompose(hand, nil, winning)
			if !ok {
				t.Fatalf("Decompose(%q, winning=%v) 应判和牌", tc.hand, winning)
			}
			if shape.Pattern != tc.wantPattern {
				t.Fatalf("Pattern = %q，期望 %q", shape.Pattern, tc.wantPattern)
			}
			if shape.Wait != tc.wantWait {
				t.Fatalf("Wait = %q，期望 %q", shape.Wait, tc.wantWait)
			}
			if !IsWinning(hand, nil, winning) {
				t.Fatal("IsWinning 与 Decompose 结果不一致")
			}
			verifyWinShape(t, hand, shape, winning)
		})
	}
}

// TestDecomposeShapeDetail 精确断言一组标准型拆解的将牌与面子内容。
func TestDecomposeShapeDetail(t *testing.T) {
	hand := mustParseHand(t, "123m 456m 789m 11p 456s")
	winning := tileFromName(t, "6s")
	shape, ok := Decompose(hand, nil, winning)
	if !ok {
		t.Fatal("应判和牌")
	}
	if shape.Pattern != PatternStandard {
		t.Fatalf("Pattern = %q", shape.Pattern)
	}
	if shape.Jantou.Kind != tile.Pin1 {
		t.Fatalf("将牌 = %v，期望 1p", shape.Jantou)
	}
	if shape.Wait != WaitRyanmen {
		t.Fatalf("Wait = %q，期望 ryanmen", shape.Wait)
	}
	wantKinds := []struct {
		kind  string
		tiles [3]tile.Kind
	}{
		{MeldChi, [3]tile.Kind{tile.Man1, tile.Man2, tile.Man3}},
		{MeldChi, [3]tile.Kind{tile.Man4, tile.Man5, tile.Man6}},
		{MeldChi, [3]tile.Kind{tile.Man7, tile.Man8, tile.Man9}},
		{MeldChi, [3]tile.Kind{tile.Sou4, tile.Sou5, tile.Sou6}},
	}
	if len(shape.Melds) != len(wantKinds) {
		t.Fatalf("面子数 = %d，期望 %d", len(shape.Melds), len(wantKinds))
	}
	for i, want := range wantKinds {
		meld := shape.Melds[i]
		if meld.Kind != want.kind {
			t.Fatalf("第 %d 组面子 Kind = %q，期望 %q", i, meld.Kind, want.kind)
		}
		for j, kind := range want.tiles {
			if meld.Tiles[j].Kind != kind {
				t.Fatalf("第 %d 组面子第 %d 张 = %v，期望 %v", i, j, meld.Tiles[j].Kind, kind)
			}
		}
	}
}

// TestDecomposeSevenPairs 覆盖七对子（日麻严格语义：必须是 7 种不同的对子）。
func TestDecomposeSevenPairs(t *testing.T) {
	cases := []struct {
		name    string
		hand    string
		winning string
	}{
		{"普通七对", "11m 22m 33m 44p 55p 66s 11z", "1z"},
		{"字牌七对", "11z 22z 33z 44z 55z 66z 77z", "1z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			winning := tileFromName(t, tc.winning)
			shape, ok := Decompose(hand, nil, winning)
			if !ok {
				t.Fatalf("Decompose(%q) 应判七对和牌", tc.hand)
			}
			if shape.Pattern != PatternSevenPairs {
				t.Fatalf("Pattern = %q，期望 %q", shape.Pattern, PatternSevenPairs)
			}
			if shape.Wait != WaitTanki {
				t.Fatalf("七对的待ち = %q，期望 tanki", shape.Wait)
			}
			if len(shape.Melds) != 0 {
				t.Fatalf("七对应没有面子，实际 %d 组", len(shape.Melds))
			}
			if shape.Jantou.Kind != winning.Kind {
				t.Fatalf("Jantou = %v，期望和了牌 %v", shape.Jantou, winning)
			}
			verifyWinShape(t, hand, shape, winning)
		})
	}
}

// TestSevenPairsStrictVsCompat 定案七对语义：
//
//   - 日麻严格（默认 / StrictOptions）：4 张相同不能算两对，必须 7 种不同的对子；
//     所以「1111m + 5 对」在默认模式下**不是**七对和牌。
//   - v1 兼容（CompatOptions）：4 张相同算两对，同样的手牌判七对和牌，
//     用于让 fastapi/go-engine 的 v1 金标 fixture 继续对拍。
//
// 这正是 task-4 要求「默认严格、v1 回归用兼容模式」的落点。
func TestSevenPairsStrictVsCompat(t *testing.T) {
	hands := []string{
		"1111m 22p 33p 44s 55s 11z", // 4 + 5 对 = 14 张
		"1111m 2222p 33s 44s 11z",   // 两组四张 + 3 对
	}
	for _, text := range hands {
		t.Run(text, func(t *testing.T) {
			hand := mustParseHand(t, text)
			winning := hand[0]

			if shape, ok := Decompose(hand, nil, winning); ok {
				t.Fatalf("严格模式不应把四张相同算两对，实际拆解出 %+v", shape)
			}
			shape, ok := DecomposeWithOptions(hand, nil, winning, CompatOptions())
			if !ok {
				t.Fatal("v1 兼容模式应判七对和牌")
			}
			if shape.Pattern != PatternSevenPairs {
				t.Fatalf("兼容模式 Pattern = %q，期望 %q", shape.Pattern, PatternSevenPairs)
			}
		})
	}

	// 13 张豪华七对听：1111m 22p 33p 44s 55s E，兼容模式向听 0；严格模式不是 0。
	tenpai := mustParseHand(t, "1111m 22p 33p 44s 55s 1z")
	if got := ShantenWithOptions(tenpai, 0, CompatOptions()); got != 0 {
		t.Fatalf("兼容模式向听 = %d，期望 0", got)
	}
	if got := Shanten(tenpai, 0); got == 0 {
		t.Fatalf("严格模式下 4 张相同不算两对，向听不应为 0（实际 %d）", got)
	}
}

// TestDecomposeKokushi 覆盖国士无双。
func TestDecomposeKokushi(t *testing.T) {
	hand := mustParseHand(t, "19m 19p 19s 1234567z 1m")
	winning := tileFromName(t, "1m")
	shape, ok := Decompose(hand, nil, winning)
	if !ok {
		t.Fatal("国士无双应判和牌")
	}
	if shape.Pattern != PatternKokushi {
		t.Fatalf("Pattern = %q，期望 %q", shape.Pattern, PatternKokushi)
	}
	if shape.Wait != WaitTanki {
		t.Fatalf("国士的待ち = %q，期望 tanki", shape.Wait)
	}
	if len(shape.Melds) != 0 {
		t.Fatalf("国士应没有面子，实际 %d 组", len(shape.Melds))
	}
	verifyWinShape(t, hand, shape, winning)
}

// TestDecomposePatternPriority 覆盖 v1 语义：七对与标准型同时成立时返回 standard。
func TestDecomposePatternPriority(t *testing.T) {
	hand := mustParseHand(t, "11223344556677m")
	winning := tileFromName(t, "7m")
	shape, ok := Decompose(hand, nil, winning)
	if !ok {
		t.Fatal("应判和牌")
	}
	if shape.Pattern != PatternStandard {
		t.Fatalf("Pattern = %q，期望 standard（标准型优先，与 v1 一致）", shape.Pattern)
	}
}

// TestDecomposeInvalidInput 覆盖 Decompose 应当拒绝的输入：张数不对、牌面非法、和了牌不匹配、副露未支持。
func TestDecomposeInvalidInput(t *testing.T) {
	fourteen := mustParseHand(t, "123456789m 123p 99s")
	eleven := mustParseHand(t, "123m 456m 789m 99s")
	unknown := append(cloneTiles(fourteen[:13]), tileFromName(t, "?"))
	overCopies := append([]tile.Tile{
		tileFromName(t, "1m"), tileFromName(t, "1m"), tileFromName(t, "1m"),
		tileFromName(t, "1m"), tileFromName(t, "1m"),
	}, mustParseHand(t, "23456789m 1p")...)

	cases := []struct {
		name    string
		hand    []tile.Tile
		melds   []Meld
		winning tile.Tile
	}{
		{"空手牌", nil, nil, tileFromName(t, "1m")},
		{"1 张", mustParseHand(t, "1m"), nil, tileFromName(t, "1m")},
		{"13 张听牌形也不能直接判胡", mustParseHand(t, "123m 456m 789m 11p 45s"), nil, tileFromName(t, "3s")},
		{"15 张", mustParseHand(t, "123456789m 123p 99s 1m"), nil, tileFromName(t, "1m")},
		{"含未知牌", unknown, nil, tileFromName(t, "1m")},
		{"同种 5 张", overCopies, nil, tileFromName(t, "1m")},
		{"和了牌不在手牌", fourteen, nil, tileFromName(t, "1z")},
		{"和了牌 Kind 越界", fourteen, nil, tile.Tile{Kind: tile.NumKinds}},
		{"副露与暗牌张数不匹配", fourteen, []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
			tileFromName(t, "1p"), tileFromName(t, "1p"), tileFromName(t, "1p"),
		}}}, tileFromName(t, "9s")},
		{"未知副露类型", eleven, []Meld{{Kind: "foo", Tiles: []tile.Tile{
			tileFromName(t, "1p"), tileFromName(t, "1p"), tileFromName(t, "1p"),
		}}}, tileFromName(t, "9s")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if shape, ok := Decompose(tc.hand, tc.melds, tc.winning); ok {
				t.Fatalf("Decompose 应返回 false，实际成功拆解出 %+v", shape)
			}
			if IsWinning(tc.hand, tc.melds, tc.winning) {
				t.Fatal("IsWinning 应返回 false")
			}
		})
	}
}

// TestDecomposeNotWinning 覆盖「牌面合法但不和牌」的 14 张手牌。
func TestDecomposeNotWinning(t *testing.T) {
	cases := []struct {
		name    string
		hand    string
		winning string
	}{
		{"散牌", "147m 147p 147s 12345z", "1z"},
		{"仅一对字牌", "147m 147p 147s 11z 234z", "1z"},
		{"六对差一对", "11m 22m 33m 44p 55p 66s 1s 2s", "1s"},
		{"字牌不能组顺子", "123z 555z 666z 777z 11m", "2z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			if len(hand) != winHandSize {
				t.Fatalf("用例手牌应为 14 张，实际 %d 张", len(hand))
			}
			winning := tileFromName(t, tc.winning)
			if shape, ok := Decompose(hand, nil, winning); ok {
				t.Fatalf("不应判和牌，实际拆解出 %+v", shape)
			}
		})
	}
}

// TestDecomposeDoesNotModifyHand 断言 Decompose / IsWinning 不修改入参。
func TestDecomposeDoesNotModifyHand(t *testing.T) {
	hand := mustParseHand(t, "123456789m 123p 99s")
	before := cloneTiles(hand)
	winning := tileFromName(t, "9s")
	if _, ok := Decompose(hand, nil, winning); !ok {
		t.Fatal("应判和牌")
	}
	if !slices.Equal(hand, before) {
		t.Fatalf("Decompose 修改了入参：%v -> %v", before, hand)
	}
}

// TestDecomposePreservesAka 断言拆解结果保留赤宝牌标记（S4 结算赤宝牌要用）。
func TestDecomposePreservesAka(t *testing.T) {
	red5s := tileFromName(t, "r5s")
	hand := mustParseHand(t, "123m 456m 789m 11p 456s")
	for i := range hand {
		if hand[i].Kind == tile.Sou5 {
			hand[i] = red5s
		}
	}
	shape, ok := Decompose(hand, nil, red5s)
	if !ok {
		t.Fatal("应判和牌")
	}
	found := false
	for _, meld := range shape.Melds {
		for _, mt := range meld.Tiles {
			if mt.Kind == tile.Sou5 && mt.Aka {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("拆解结果里应保留赤 5s 标记")
	}

	// 赤 5 做成将牌时也必须保留标记。
	red5p := tileFromName(t, "r5p")
	pairHand := mustParseHand(t, "123m 456m 789m 55p 456s")
	for i := range pairHand {
		if pairHand[i].Kind == tile.Pin5 {
			pairHand[i] = red5p
		}
	}
	pairShape, ok := Decompose(pairHand, nil, red5p)
	if !ok {
		t.Fatal("应判和牌")
	}
	if !pairShape.Jantou.Aka || pairShape.Jantou.Kind != tile.Pin5 {
		t.Fatalf("将牌 = %+v，期望带赤标记的 5p", pairShape.Jantou)
	}
}

// TestDecomposeNoAkaWhenNoRedTile 断言没有赤牌时不会凭空产生 Aka 标记。
func TestDecomposeNoAkaWhenNoRedTile(t *testing.T) {
	hand := mustParseHand(t, "123m 456m 789m 11p 456s")
	shape, ok := Decompose(hand, nil, tileFromName(t, "6s"))
	if !ok {
		t.Fatal("应判和牌")
	}
	for _, mt := range flattenShape(shape) {
		if mt.Aka {
			t.Fatalf("手牌里没有赤宝牌，但拆解结果出现了 Aka 标记：%v", mt)
		}
	}
}
