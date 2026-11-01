package mahjong

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 本文件是 Go 侧的性能基准，输入与 Python 侧 timeit 共用
// go-engine/testdata/bench_cases.json，保证两边跑的是同一批手牌。
//
// 跑法（在 go-engine 目录）：
//
//	go test ./mahjong -bench . -benchmem -run '^$'
//
// b.ReportAllocs() 让输出带上 B/op 与 allocs/op：
//   - B/op 是每次操作申请的堆内存字节数，用来判断实现有没有多余分配；
//   - allocs/op 是每次操作的分配次数，0 表示热点路径完全没碰堆（GC 友好）。
//
// 这个意义在 Go 服务里很实际：胡牌判定是每个请求都会走的 CPU 热点，
// 若 0 allocs/op，则并发请求不会额外制造 GC 压力。

// 防止编译器把基准循环里的结果优化掉：把结果写进包级变量。
var (
	sinkPattern Pattern
	sinkWaits   []Tile
)

// benchCase 是 benchmark 输入文件里的一条用例。
type benchCase struct {
	ID   string `json:"id"`
	Hand string `json:"hand"`
}

// benchCaseFile 是 bench_cases.json 的顶层结构。
type benchCaseFile struct {
	Win  []benchCase `json:"win"`
	Wait []benchCase `json:"wait"`
}

// loadBenchCases 读取共享的 benchmark 用例。
func loadBenchCases(b *testing.B) benchCaseFile {
	b.Helper()
	path := filepath.Join("..", "testdata", "bench_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("读取 benchmark 用例失败（%s）：%v", path, err)
	}
	var cases benchCaseFile
	if err := json.Unmarshal(data, &cases); err != nil {
		b.Fatalf("解析 benchmark 用例失败（%s）：%v", path, err)
	}
	if len(cases.Win) == 0 || len(cases.Wait) == 0 {
		b.Fatalf("benchmark 用例为空（%s）", path)
	}
	return cases
}

// mustParseBenchHand 解析 benchmark 手牌，失败直接终止基准。
func mustParseBenchHand(b *testing.B, text string) []Tile {
	b.Helper()
	hand, err := ParseHand(text)
	if err != nil {
		b.Fatalf("解析手牌 %q 失败：%v", text, err)
	}
	return hand
}

// BenchmarkWinningPattern 基准：14 张手牌的胡牌判定。
//
// 用例覆盖标准型、回溯拆解、七对、字牌刻子、散牌与多刻子未胡；
// 每次迭代只做一次 WinningPattern 调用（含完整入参校验），
// 与 Python 侧 timeit(lambda: winning_pattern(hand)) 完全对应。
func BenchmarkWinningPattern(b *testing.B) {
	for _, tc := range loadBenchCases(b).Win {
		b.Run(tc.ID, func(b *testing.B) {
			hand := mustParseBenchHand(b, tc.Hand)
			b.ReportAllocs()
			b.ResetTimer()
			var pattern Pattern
			for i := 0; i < b.N; i++ {
				pattern, _ = WinningPattern(hand)
			}
			b.StopTimer()
			sinkPattern = pattern
		})
	}
}

// BenchmarkWaitingTiles 基准：13 张手牌的听牌计算。
//
// 每次调用会试加 34 种候选牌并逐一判定，是比胡牌判定更重的压力用例；
// 覆盖两面听、九面听、七对听、不听、已有 4 张不推荐五种情况。
func BenchmarkWaitingTiles(b *testing.B) {
	for _, tc := range loadBenchCases(b).Wait {
		b.Run(tc.ID, func(b *testing.B) {
			hand := mustParseBenchHand(b, tc.Hand)
			b.ReportAllocs()
			b.ResetTimer()
			var waits []Tile
			for i := 0; i < b.N; i++ {
				waits, _ = WaitingTiles(hand)
			}
			b.StopTimer()
			sinkWaits = waits
		})
	}
}
