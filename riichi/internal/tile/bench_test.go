package tile

import "testing"

// 基准里的 sink：把最后一次结果写进包级变量，避免编译器把调用优化掉，
// 也方便人工核对基准跑的是同一份输入。
var (
	sinkDeck []Tile
	sinkWall *Wall
)

// BenchmarkBuildDeck 测「构造 136 张牌 + 标记 3 枚赤宝牌」的开销。
// 这是开局一次性成本，但它是牌山所有后续操作的输入，值得量化。
func BenchmarkBuildDeck(b *testing.B) {
	b.ReportAllocs()
	var deck []Tile
	for i := 0; i < b.N; i++ {
		deck, _ = BuildDeck(3)
	}
	sinkDeck = deck
}

// BenchmarkShuffle 测「136 张牌的 Fisher-Yates 原地洗牌」（SplitMix64，无额外分配）。
// 洗牌每局只做一次，但 136 次交换 + 拒绝采样取模是确定性回放的基本盘。
func BenchmarkShuffle(b *testing.B) {
	deck, err := BuildDeck(3)
	if err != nil {
		b.Fatalf("BuildDeck(3) error: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Shuffle(deck, 42)
	}
	b.StopTimer()
	sinkDeck = deck
}

// BenchmarkNewWall 测「建牌 + 洗牌 + 切活牌/王牌」的整条组墙路径，
// 对应每局开局调用一次 tile.NewWall(seed, redFives)。
func BenchmarkNewWall(b *testing.B) {
	b.ReportAllocs()
	var w *Wall
	for i := 0; i < b.N; i++ {
		w, _ = NewWall(20261110, 3)
	}
	sinkWall = w
}
