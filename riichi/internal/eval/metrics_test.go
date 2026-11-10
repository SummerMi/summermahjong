package eval

import (
	"encoding/json"
	"math"
	"testing"

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
