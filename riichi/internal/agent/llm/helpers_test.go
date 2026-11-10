package llm

import (
	"testing"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// mustTiles 把牌面文本解析成 tile.Tile 列表，解析失败直接 fail 测试。
func mustTiles(t *testing.T, codes ...string) []tile.Tile {
	t.Helper()
	out := make([]tile.Tile, 0, len(codes))
	for _, code := range codes {
		parsed, err := tile.ParseTile(code)
		if err != nil {
			t.Fatalf("parse tile %q: %v", code, err)
		}
		out = append(out, parsed)
	}
	return out
}

// defaultHand14 是一手 14 张手牌：123m 456m 789m 11p 23p 4p。
//
// 打出 4p 后是 123m456m789m11p23p 的 1p/4p 听牌，适合做「切牌候选」测试。
var defaultHand14 = []string{
	"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m",
	"1p", "1p", "2p", "3p", "4p",
}

// makeObs 构造一个 14 张手牌、全部可打出的 Observation（座位 0）。
func makeObs(t *testing.T, handCodes []string, options ...func(*game.Observation)) game.Observation {
	t.Helper()
	hand := mustTiles(t, handCodes...)
	obs := game.Observation{
		Seat:          0,
		Hand:          hand,
		WallRemaining: 70,
		Scores:        [4]int{25000, 25000, 25000, 25000},
	}
	for _, handTile := range hand {
		obs.LegalActions = append(obs.LegalActions, game.Action{Kind: game.ActDiscard, Seat: obs.Seat, Tile: handTile})
	}
	for _, option := range options {
		option(&obs)
	}
	return obs
}

// findCandidate 在候选列表里找某张牌，找不到返回 nil。
func findCandidate(candidates []Candidate, code string) *Candidate {
	for i := range candidates {
		if candidates[i].Tile == code {
			return &candidates[i]
		}
	}
	return nil
}
