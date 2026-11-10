package eval

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"summermahjong/riichi/internal/game"
)

func approxEqual(t *testing.T, got, want, tol float64, label string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s: got %.6f want %.6f (±%.6f)", label, got, want, tol)
	}
}

func TestNewRate(t *testing.T) {
	rate := NewRate(25, 100)
	approxEqual(t, rate.Value, 0.25, 1e-12, "Value")
	// se = sqrt(0.25*0.75/100) = 0.043301…；±1.96·se = ±0.08487
	approxEqual(t, rate.Low, 0.25-1.959963984540054*math.Sqrt(0.25*0.75/100), 1e-9, "Low")
	approxEqual(t, rate.High, 0.25+1.959963984540054*math.Sqrt(0.25*0.75/100), 1e-9, "High")

	// 全中/全不中：区间应截断在 [0,1]
	all := NewRate(10, 10)
	if all.Low < 0 || all.High > 1 || all.High < 0.99 {
		t.Fatalf("10/10 的区间不合理: %+v", all)
	}
	none := NewRate(0, 10)
	if none.Value != 0 || none.Low != 0 {
		t.Fatalf("0/10 的区间不合理: %+v", none)
	}
	// 分母为 0
	empty := NewRate(3, 0)
	if empty.Den != 0 || empty.Value != 0 || empty.Low != 0 || empty.High != 0 {
		t.Fatalf("分母为 0 时应全零: %+v", empty)
	}
}

func TestNewMeanCI(t *testing.T) {
	// 样本 [2,4]：mean=3，样本方差=2，se=1，margin=1.96
	stat := NewMeanCI(6, 20, 2)
	approxEqual(t, stat.Mean, 3, 1e-12, "Mean")
	approxEqual(t, stat.StdDev, math.Sqrt2, 1e-12, "StdDev")
	approxEqual(t, stat.Low, 3-1.959963984540054, 1e-9, "Low")
	approxEqual(t, stat.High, 3+1.959963984540054, 1e-9, "High")

	// n=1：区间退化为点值
	one := NewMeanCI(4, 16, 1)
	if one.Mean != 4 || one.Low != 4 || one.High != 4 {
		t.Fatalf("n=1 应退化为点值: %+v", one)
	}
	// n=0：零值
	zero := NewMeanCI(10, 100, 0)
	if zero.N != 0 || zero.Mean != 0 {
		t.Fatalf("n=0 应为零值: %+v", zero)
	}
}

func TestDurationStatPercentiles(t *testing.T) {
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(i + 1)
	}
	stat := NewDurationStat(values)
	if stat.Count != 100 {
		t.Fatalf("Count = %d", stat.Count)
	}
	approxEqual(t, stat.MeanMs, 50.5, 1e-12, "Mean")
	approxEqual(t, stat.P50Ms, 50, 1e-12, "P50")
	approxEqual(t, stat.P95Ms, 95, 1e-12, "P95")
	approxEqual(t, stat.MaxMs, 100, 1e-12, "Max")

	// 输入顺序不影响分位（内部会排序），且不修改入参
	shuffled := []float64{30, 10, 20}
	stat2 := NewDurationStat(shuffled)
	approxEqual(t, stat2.P50Ms, 20, 1e-12, "P50 shuffled")
	approxEqual(t, stat2.P95Ms, 30, 1e-12, "P95 shuffled")
	if shuffled[0] != 30 || shuffled[1] != 10 {
		t.Fatalf("NewDurationStat 不应修改入参: %v", shuffled)
	}
	if empty := NewDurationStat(nil); empty.Count != 0 || empty.P50Ms != 0 {
		t.Fatalf("空样本应为零值: %+v", empty)
	}
	if got := percentileSorted(nil, 0.5); got != 0 {
		t.Fatalf("空切片分位应为 0，实际 %v", got)
	}
	if got := percentileSorted([]float64{1, 2, 3}, 0); got != 1 {
		t.Fatalf("p=0 应取最小值，实际 %v", got)
	}
	if got := percentileSorted([]float64{1, 2, 3}, 1.0); got != 3 {
		t.Fatalf("p=1 应取最大值，实际 %v", got)
	}
}

func TestRanksOf(t *testing.T) {
	ranks := ranksOf([4]int{30000, 25000, 25000, 10000})
	want := [4]int{1, 2, 2, 4}
	if ranks != want {
		t.Fatalf("ranks = %v want %v", ranks, want)
	}
	// 全同分：所有座位都是第 1
	if got := ranksOf([4]int{25000, 25000, 25000, 25000}); got != [4]int{1, 1, 1, 1} {
		t.Fatalf("全同分应为全 1，实际 %v", got)
	}
}

func mustPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func TestTallyRoundEvents(t *testing.T) {
	state := &game.State{}
	state.Events = []game.Event{
		{Seq: 1, Kind: game.EventRoundStart, Seat: 0},
		{Seq: 2, Kind: game.EventDiscard, Seat: 0},
		{Seq: 3, Kind: game.EventRiichi, Seat: 1},
		{Seq: 4, Kind: game.EventDiscard, Seat: 1},
		{Seq: 5, Kind: game.EventChi, Seat: 2},
		{Seq: 6, Kind: game.EventDiscard, Seat: 2},
		{Seq: 7, Kind: game.EventKan, Seat: 3, Payload: mustPayload(t, map[string]any{"meld_kind": "kan_ankan"})},
		{Seq: 8, Kind: game.EventTsumo, Seat: 1, Payload: mustPayload(t, winPayload{Points: 1000, Han: 2})},
		{Seq: 9, Kind: game.EventDiscard, Seat: 0},
		{Seq: 10, Kind: game.EventRon, Seat: 3, Payload: mustPayload(t, winPayload{Points: 13000, Han: 13})},
		{Seq: 11, Kind: game.EventRyuukyoku, Seat: 0},
	}
	res := &GameResult{}
	tallyRound(res, state, 0)

	if res.Seats[1].Wins != 1 || res.Seats[1].TsumoWins != 1 || res.Seats[1].WinPoints != 1000 {
		t.Fatalf("seat1 自摸统计错误: %+v", res.Seats[1])
	}
	if res.Seats[1].Riichi != 1 {
		t.Fatalf("seat1 立直统计错误: %+v", res.Seats[1])
	}
	if res.Seats[2].Melds != 1 {
		t.Fatalf("seat2 吃应计一次副露: %+v", res.Seats[2])
	}
	if res.Seats[3].Ankan != 1 || res.Seats[3].Melds != 0 {
		t.Fatalf("seat3 暗杠应只计 ankan: %+v", res.Seats[3])
	}
	if res.Seats[3].Wins != 1 || res.Seats[3].RonWins != 1 || res.Seats[3].Yakuman != 1 {
		t.Fatalf("seat3 荣和/役满统计错误: %+v", res.Seats[3])
	}
	if res.Seats[3].OpenWins != 1 {
		t.Fatalf("seat3 有暗杠，应记为近似计分和了: %+v", res.Seats[3])
	}
	// 放铳归属：seat0 打出后 seat3 荣和
	if res.Seats[0].Deals != 1 {
		t.Fatalf("seat0 应记一次放铳，实际 %+v", res.Seats[0])
	}
	// 只统计 from 之后的事件
	res2 := &GameResult{}
	tallyRound(res2, state, 10)
	if res2.Seats != ([4]SeatRoundStats{}) {
		t.Fatalf("from 之后的窗口应只含轮末事件: %+v", res2.Seats)
	}
}

func TestSummarize(t *testing.T) {
	cfg := Config{
		Agents: []string{"shanten:attack", "defense:defense", "search:closed", "random"},
	}
	games := []GameResult{
		{
			Seed:        1,
			Rounds:      2,
			FinalScores: [4]int{40000, 20000, 20000, 20000},
			Ranks:       ranksOf([4]int{40000, 20000, 20000, 20000}),
			Seats: [4]SeatRoundStats{
				{Wins: 2, WinPoints: 8000, Decisions: 10, Riichi: 1, Melds: 2},
				{Decisions: 12, Deals: 2},
				{Decisions: 11},
				{Decisions: 13, OpenWins: 1, Wins: 1, WinPoints: 1000},
			},
			LatenciesMs: [4][]float64{{1, 2}, {}, {}, {}},
		},
		{
			Seed:        2,
			Rounds:      2,
			FinalScores: [4]int{20000, 40000, 20000, 20000},
			Ranks:       ranksOf([4]int{20000, 40000, 20000, 20000}),
			Seats: [4]SeatRoundStats{
				{Decisions: 9, Yakuman: 1, Wins: 1, WinPoints: 32000},
				{Decisions: 10},
				{Decisions: 8},
				{Decisions: 7},
			},
		},
	}
	summary := summarize(cfg, games)

	if summary.Games != 2 || summary.Rounds != 4 {
		t.Fatalf("Games/Rounds 汇总错误: %+v", summary)
	}
	seat0 := summary.Seats[0]
	if seat0.Agent != "shanten:attack" || seat0.Rounds != 4 || seat0.Games != 2 {
		t.Fatalf("seat0 基本信息错误: %+v", seat0)
	}
	approxEqual(t, seat0.WinRate.Value, 3.0/4.0, 1e-12, "seat0 和了率")
	approxEqual(t, seat0.RiichiRate.Value, 0.25, 1e-12, "seat0 立直率")
	approxEqual(t, seat0.MeldRate.Value, 0.5, 1e-12, "seat0 副露率")
	approxEqual(t, seat0.AvgRank.Mean, (1.0+2.0)/2, 1e-12, "seat0 平均顺位")
	approxEqual(t, seat0.Decision.P50Ms, 1.0, 1e-12, "seat0 P50")
	approxEqual(t, seat0.Decision.P95Ms, 2.0, 1e-12, "seat0 P95")
	if len(summary.Elo.Ratings) != 4 {
		t.Fatalf("ELO 应覆盖 4 个 agent: %+v", summary.Elo)
	}
	// seats[1] 在第二局不存在（只打了 1 局有效顺位）？两局都有效，所以是 2 局。
	if summary.Seats[1].Games != 2 {
		t.Fatalf("seat1 Games = %d", summary.Seats[1].Games)
	}
	if summary.Seats[1].AvgRank.Mean != (2.0+1.0)/2 {
		t.Fatalf("seat1 平均顺位错误: %+v", summary.Seats[1].AvgRank)
	}
}

func TestSummarizeSkipsRankForAbortedGames(t *testing.T) {
	cfg := Config{Agents: []string{"a", "b", "c", "d"}}
	games := []GameResult{
		{Seed: 1, Rounds: 1, Aborted: true, Error: "boom"},
		{Seed: 2, Rounds: 1, FinalScores: [4]int{1, 2, 3, 4}, Ranks: [4]int{4, 3, 2, 1}},
	}
	summary := summarize(cfg, games)
	if summary.ErrorN != 1 {
		t.Fatalf("ErrorN = %d", summary.ErrorN)
	}
	if summary.Seats[0].Games != 1 {
		t.Fatalf("aborted 场次不应计入 Games: %+v", summary.Seats[0])
	}
	// 轮数仍然计入（已经打完的轮是有数据的）
	if summary.Seats[0].Rounds != 2 {
		t.Fatalf("Rounds 应包含 aborted 场的已完成轮数: %+v", summary.Seats[0])
	}
}

// ────────────────────────────────────────────────────────────────────────────
// 除零 / 无样本边界：JSON 必须输出 null，绝不出现 NaN/Inf（原 bug：
// `--calls=agent` 时副露次数/轮 >1 → p(1-p)<0 → 二项 CI 的 sqrt 得 NaN → 写报告失败）。
// ────────────────────────────────────────────────────────────────────────────

// marshalFinite 序列化并断言结果里没有 NaN/Inf 字面量。
func marshalFinite(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal 失败（极可能出现了 NaN/Inf）: %v", err)
	}
	body := string(data)
	for _, bad := range []string{"NaN", "+Inf", "-Inf"} {
		if strings.Contains(body, bad) {
			t.Fatalf("JSON 里出现了非法值 %s: %s", bad, body)
		}
	}
	return body
}

func TestRateJSONEdgeCases(t *testing.T) {
	// 1) 分母为 0 → 整个指标为 null
	if body := marshalFinite(t, NewRate(0, 0)); body != "null" {
		t.Fatalf("分母 0 应序列化为 null，实际 %s", body)
	}
	if NewRate(0, 0).HasData() {
		t.Fatalf("分母 0 时 HasData 应为 false")
	}

	// 2) 计数型指标（副露次数/轮，>1）：value 保留、CI 为 null，不再出现 NaN
	rate := NewRate(143, 58)
	if math.IsNaN(rate.Low) || math.IsNaN(rate.High) {
		t.Fatalf("NewRate 不应产生 NaN: %+v", rate)
	}
	body := marshalFinite(t, rate)
	if !strings.Contains(body, `"value":2.4655`) {
		t.Fatalf("计数型指标的 value 应保留，实际 %s", body)
	}
	if !strings.Contains(body, `"ci_low":null`) || !strings.Contains(body, `"ci_high":null`) {
		t.Fatalf("计数型指标没有有意义的二项 CI，应输出 null: %s", body)
	}

	// 3) 正常比例仍带 CI
	normal := marshalFinite(t, NewRate(25, 100))
	if !strings.Contains(normal, `"ci_low":0.165`) || !strings.Contains(normal, `"ci_high":0.3348`) {
		t.Fatalf("正常比例的 CI 不应丢失: %s", normal)
	}
}

func TestMeanCIJSONEdgeCases(t *testing.T) {
	if body := marshalFinite(t, NewMeanCI(0, 0, 0)); body != "null" {
		t.Fatalf("n=0 应序列化为 null，实际 %s", body)
	}
	if NewMeanCI(0, 0, 0).HasData() {
		t.Fatalf("n=0 时 HasData 应为 false")
	}
	body := marshalFinite(t, NewMeanCI(6, 20, 2))
	if !strings.Contains(body, `"mean":3`) || !strings.Contains(body, `"n":2`) {
		t.Fatalf("均值 JSON 不符合预期: %s", body)
	}
}

func TestDurationStatJSONEdgeCases(t *testing.T) {
	if body := marshalFinite(t, NewDurationStat(nil)); body != "null" {
		t.Fatalf("空样本应序列化为 null，实际 %s", body)
	}
	if NewDurationStat(nil).HasData() {
		t.Fatalf("空样本 HasData 应为 false")
	}
	body := marshalFinite(t, NewDurationStat([]float64{1, 2, 3}))
	if !strings.Contains(body, `"count":3`) || !strings.Contains(body, `"p50_ms":2`) {
		t.Fatalf("耗时统计 JSON 不符合预期: %s", body)
	}
}

// TestSummaryJSONBoundaries 覆盖 0 局 / 0 和了 / 0 立直 / 0 决策四种边界。
func TestSummaryJSONBoundaries(t *testing.T) {
	cfg := Config{Agents: []string{"a", "b", "c", "d"}}

	// 1) 0 局：所有比率/均值/分位都应为 null
	emptyBody := marshalFinite(t, summarize(cfg, nil))
	for _, want := range []string{
		`"win_rate":null`, `"avg_rank":null`, `"avg_win_points":null`,
		`"decision_ms":null`, `"duration_ms":null`,
	} {
		if !strings.Contains(emptyBody, want) {
			t.Fatalf("0 局时缺少 %s\nJSON: %s", want, emptyBody)
		}
	}

	// 2) 有轮数但 0 和了 / 0 立直 / 0 决策
	games := []GameResult{{
		Seed:        1,
		Rounds:      2,
		FinalScores: [4]int{25000, 25000, 25000, 25000},
		Ranks:       [4]int{1, 1, 1, 1},
	}}
	body := marshalFinite(t, summarize(cfg, games))
	// 和了率/立直率有分母（轮数）：value=0 而不是 null
	if !strings.Contains(body, `"win_rate":{"num":0,"den":2,"value":0`) {
		t.Fatalf("0 和了时 win_rate 应为 0/2，实际 JSON: %s", body)
	}
	if !strings.Contains(body, `"riichi_rate":{"num":0,"den":2,"value":0`) {
		t.Fatalf("0 立直时 riichi_rate 应为 0/2，实际 JSON: %s", body)
	}
	// 平均打点（0 和了）与决策分位（0 决策）没有样本 → null
	if !strings.Contains(body, `"avg_win_points":null`) {
		t.Fatalf("0 和了时 avg_win_points 应为 null: %s", body)
	}
	if !strings.Contains(body, `"decision_ms":null`) {
		t.Fatalf("0 决策时 decision_ms 应为 null: %s", body)
	}
	if !strings.Contains(body, `"avg_rank":{"n":1`) {
		t.Fatalf("平均顺位有 1 场数据，应为对象: %s", body)
	}
}

// TestReportJSONWithCountTypeMeldRate 直接复现原 bug 场景：每轮副露次数 >1 时
// 报告仍必须能写出（CI 为 null，不再 NaN）。
func TestReportJSONWithCountTypeMeldRate(t *testing.T) {
	cfg, err := (Config{
		Seeds:      []int64{1},
		Agents:     []string{"shanten:attack", "defense:defense", "search:closed", "random"},
		RoundLimit: 1,
	}).withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	games := []GameResult{{
		Seed:        1,
		Rounds:      1,
		FinalScores: [4]int{30000, 25000, 25000, 20000},
		Ranks:       ranksOf([4]int{30000, 25000, 25000, 20000}),
		Seats: [4]SeatRoundStats{
			{Melds: 5, Decisions: 3, Wins: 1, WinPoints: 1000},
			{Melds: 4},
			{Melds: 3},
			{Melds: 2},
		},
		LatenciesMs: [4][]float64{{1, 2, 3}, {}, {}, {}},
	}}
	result := &Result{
		Config:    cfg,
		Games:     games,
		Summary:   summarize(cfg, games),
		StartedAt: time.Now(),
	}
	report := BuildReport(result, ReportOptions{SeedSpec: "1", GitSHA: "deadbeef"})
	body := marshalFinite(t, report)
	if !strings.Contains(body, `"meld_rate":{"num":5,"den":1,"value":5`) {
		t.Fatalf("副露/轮 = 5 应原样输出，实际: %s", body)
	}
	if !strings.Contains(body, `"ci_low":null`) {
		t.Fatalf("计数型指标的 CI 应为 null: %s", body)
	}
	// 全量报告 JSON 也必须能序列化（原 bug 就在这里失败）
	if !strings.Contains(body, `"manifest"`) || !strings.Contains(body, `"games"`) {
		t.Fatalf("报告结构不完整: %s", body)
	}
}
