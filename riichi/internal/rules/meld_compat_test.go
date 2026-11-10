package rules

import (
	"errors"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestOptionsDefaults 七对语义默认严格；CompatOptions 显式切到 v1（4 张算两对）。
func TestOptionsDefaults(t *testing.T) {
	if !StrictOptions().StrictSevenPairs() {
		t.Fatal("StrictOptions 应为严格七对语义")
	}
	if !(Options{}).StrictSevenPairs() {
		t.Fatal("Options 零值应为严格七对语义")
	}
	if CompatOptions().StrictSevenPairs() {
		t.Fatal("CompatOptions 不应是严格语义")
	}
}

// TestShapeTilesFallback 手工构造的 shape 未填 Tiles 时，ShapeTiles 由将牌+面子拼出。
func TestShapeTilesFallback(t *testing.T) {
	shape := WinShape{
		Jantou:  tileFromName(t, "1p"),
		Pattern: PatternStandard,
		Wait:    WaitTanki,
		Melds: []MeldShape{
			parseMeldSpec(t, "chi:123m"),
			parseMeldSpec(t, "chi:456m"),
			parseMeldSpec(t, "pon_closed:5z"),
			parseMeldSpec(t, "chi:789s"),
		},
	}
	tiles := ShapeTiles(shape)
	if len(tiles) != winHandSize {
		t.Fatalf("ShapeTiles 长度 = %d，期望 %d", len(tiles), winHandSize)
	}
	var want [tile.NumKinds]int
	want[tile.Pin1] = 2
	for _, meld := range shape.Melds {
		for _, mt := range meld.Tiles {
			want[mt.Kind]++
		}
	}
	if got := kindCounts(tiles); got != want {
		t.Fatalf("ShapeTiles 用牌 = %v，期望 %v", got, want)
	}
}

// TestShapeValidation 结构不自洽的 WinShape 必须被 Evaluate/Fu 拒绝。
func TestShapeValidation(t *testing.T) {
	valid := func(t *testing.T) WinShape {
		shape, ok := Decompose(mustParseHand(t, plainHand), nil, tileFromName(t, "6s"))
		if !ok {
			t.Fatal("plainHand 应判和")
		}
		return shape
	}

	badWait := valid(t)
	badWait.Wait = "unknown"

	badMeldKind := valid(t)
	badMeldKind.Melds[0].Kind = "unknown"

	badChi := valid(t)
	badChi.Melds[0] = MeldShape{Kind: MeldChi, Tiles: []tile.Tile{
		tileFromName(t, "1m"), tileFromName(t, "2m"), tileFromName(t, "4m"),
	}}

	badPon := valid(t)
	badPon.Melds[1] = MeldShape{Kind: MeldPon, Tiles: []tile.Tile{
		tileFromName(t, "2p"), tileFromName(t, "2p"),
	}}

	badKan := valid(t)
	badKan.Melds[2] = MeldShape{Kind: MeldKanAnkan, Tiles: []tile.Tile{
		tileFromName(t, "3s"), tileFromName(t, "3s"), tileFromName(t, "3s"),
	}}

	badTiles := valid(t)
	badTiles.Tiles = badTiles.Tiles[:len(badTiles.Tiles)-1]

	sevenPairsWithMelds := valid(t)
	sevenPairsWithMelds.Pattern = PatternSevenPairs

	cases := []struct {
		name  string
		shape WinShape
	}{
		{"未知待ち", badWait},
		{"未知面子类型", badMeldKind},
		{"顺子不连续", badChi},
		{"碰只有两张", badPon},
		{"杠只有三张", badKan},
		{"Tiles 张数不对", badTiles},
		{"七对却带面子", sevenPairsWithMelds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Fu(tc.shape, plainCtx()); !errors.Is(err, ErrInvalidShape) {
				t.Fatalf("Fu 返回 %v，期望 ErrInvalidShape", err)
			}
			if _, _, _, err := Evaluate(tc.shape, plainCtx()); !errors.Is(err, ErrInvalidShape) {
				t.Fatalf("Evaluate 返回 %v，期望 ErrInvalidShape", err)
			}
		})
	}
}

// TestDecomposeMeldOverCopies 暗牌 + 副露合起来同种超过 4 张必须拒绝。
func TestDecomposeMeldOverCopies(t *testing.T) {
	hand := mustParseHand(t, "5555z 123m 456m 1p") // 暗牌 11 张，含 4 张白
	melds := []Meld{{Kind: MeldPon, Tiles: parseTileNames(t, []string{"5z", "5z", "5z"})}}
	if _, ok := Decompose(hand, melds, tileFromName(t, "1p")); ok {
		t.Fatal("暗牌+副露里白合计 7 张，应返回 false")
	}
}
