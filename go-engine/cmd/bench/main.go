// Command bench 是 go-engine 的独立性能演示程序：不依赖 go test，
// 直接打印一批代表性手牌的单次调用耗时（ns/op 与 µs/op）。
//
// 用法（在 go-engine 目录下）：
//
//	go run ./cmd/bench
//	go run ./cmd/bench -iter 200000   # 固定迭代次数（默认自动校准到约 100ms）
//	go run ./cmd/bench -cases ../go-engine/testdata/bench_cases.json
//
// 输入用例与 go test 的基准、Python 侧 timeit 完全共用同一份
// testdata/bench_cases.json，避免「两边比的不是同一批数据」。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gd-mahjong-go/mahjong"
)

// benchCase 是输入文件里的一条用例。
type benchCase struct {
	ID   string `json:"id"`
	Hand string `json:"hand"`
}

// benchCaseFile 是 bench_cases.json 的顶层结构。
type benchCaseFile struct {
	Win  []benchCase `json:"win"`
	Wait []benchCase `json:"wait"`
}

// item 是一条可测量的用例（手牌已解析成 Tile）。
type item struct {
	group string
	id    string
	hand  []mahjong.Tile
}

// 包级 sink：防止编译器把测量循环优化成空循环。
var (
	sinkPattern mahjong.Pattern
	sinkWaits   []mahjong.Tile
)

func main() {
	iterFlag := flag.Int("iter", 0, "每个用例的固定迭代次数；0 表示自动校准到约 100ms")
	casesFlag := flag.String("cases", "", "bench_cases.json 路径；默认自动在 testdata/ 与 go-engine/testdata/ 下查找")
	flag.Parse()

	path, err := resolveCasesPath(*casesFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "定位用例文件失败：", err)
		os.Exit(1)
	}
	cases, err := loadCases(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载用例失败：", err)
		os.Exit(1)
	}

	fmt.Printf("Go %s，单线程顺序执行；每项计时包含完整调用（含入参校验）\n", runtime.Version())
	fmt.Printf("用例文件：%s\n\n", path)
	fmt.Printf("%-32s %12s %14s %10s\n", "用例", "迭代次数", "ns/op", "µs/op")
	fmt.Printf("%s\n", "----------------------------------------------------------------------")

	var totalNS int64
	for _, c := range cases {
		iters, perOp := measure(c, *iterFlag)
		totalNS += perOp.Nanoseconds()
		fmt.Printf("%-32s %12d %14.1f %10.3f\n",
			c.group+"/"+c.id, iters, float64(perOp.Nanoseconds()), float64(perOp.Nanoseconds())/1000)
	}
	fmt.Printf("%s\n", "----------------------------------------------------------------------")
	fmt.Printf("共 %d 个用例，单次调用平均 %.3f µs\n",
		len(cases), float64(totalNS)/float64(len(cases))/1000)
}

// resolveCasesPath 依次尝试显式路径与常见相对路径。
func resolveCasesPath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}
	candidates := []string{
		filepath.Join("testdata", "bench_cases.json"),
		filepath.Join("go-engine", "testdata", "bench_cases.json"),
		filepath.Join("..", "testdata", "bench_cases.json"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到 bench_cases.json（已尝试 %v）", candidates)
}

// loadCases 读取并解析用例，顺带校验张数（14 张给胡牌判定、13 张给听牌计算）。
func loadCases(path string) ([]item, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload benchCaseFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if len(payload.Win) == 0 || len(payload.Wait) == 0 {
		return nil, fmt.Errorf("用例文件 %s 缺少 win 或 wait 分组", path)
	}

	items := make([]item, 0, len(payload.Win)+len(payload.Wait))
	add := func(group string, cases []benchCase, wantSize int) error {
		for _, c := range cases {
			hand, err := mahjong.ParseHand(c.Hand)
			if err != nil {
				return fmt.Errorf("用例 %s 解析失败：%w", c.ID, err)
			}
			if len(hand) != wantSize {
				return fmt.Errorf("用例 %s 有 %d 张牌，期望 %d 张", c.ID, len(hand), wantSize)
			}
			items = append(items, item{group: group, id: c.ID, hand: hand})
		}
		return nil
	}
	if err := add("win", payload.Win, mahjong.WinHandSize); err != nil {
		return nil, err
	}
	if err := add("wait", payload.Wait, mahjong.TenpaiHandSize); err != nil {
		return nil, err
	}
	return items, nil
}

// run 执行一次被测调用，结果写入包级 sink。
func run(c item) {
	switch c.group {
	case "win":
		sinkPattern, _ = mahjong.WinningPattern(c.hand)
	case "wait":
		sinkWaits, _ = mahjong.WaitingTiles(c.hand)
	}
}

// measure 先预热，再按固定次数（或自动校准到约 100ms）测量单次耗时。
func measure(c item, fixedIters int) (int, time.Duration) {
	const (
		minDuration = 100 * time.Millisecond
		maxIters    = 1 << 28
	)
	body := func() { run(c) }
	for i := 0; i < 2000; i++ { // 预热，让 CPU 频率与分支预测稳定
		body()
	}

	iters := fixedIters
	if iters > 0 {
		return iters, timeCost(body, iters)
	}

	iters = 1
	for {
		elapsed := timeCost(body, iters)
		if elapsed >= minDuration || iters >= maxIters {
			return iters, elapsed / time.Duration(iters)
		}
		factor := int(minDuration / elapsed)
		if factor < 2 {
			factor = 2
		}
		if factor > 64 {
			factor = 64
		}
		iters *= factor
		if iters > maxIters {
			iters = maxIters
		}
	}
}

// timeCost 测量执行 iters 次 body 的总耗时（至少返回 1ns，避免除零）。
func timeCost(body func(), iters int) time.Duration {
	start := time.Now()
	for i := 0; i < iters; i++ {
		body()
	}
	elapsed := time.Since(start)
	if elapsed <= 0 {
		return time.Nanosecond
	}
	return elapsed
}
