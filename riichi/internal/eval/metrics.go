package eval

import (
	"encoding/json"
	"math"
	"sort"

	"summermahjong/riichi/internal/game"
)

// z95 是 95% 置信区间的正态分位数（1.959964…）。
const z95 = 1.959963984540054

// Rate 是带 95% 置信区间的比例（正态近似：p ± 1.96·√(p(1-p)/n)）。
type Rate struct {
	Num   int64   `json:"num"`
	Den   int64   `json:"den"`
	Value float64 `json:"value"`
	Low   float64 `json:"ci_low"`
	High  float64 `json:"ci_high"`
}

// NewRate 构造比例与置信区间；分母为 0 时全部为 0。
func NewRate(num, den int64) Rate {
	r := Rate{Num: num, Den: den}
	if den <= 0 {
		return r
	}
	p := float64(num) / float64(den)
	r.Value = p
	se := math.Sqrt(p * (1 - p) / float64(den))
	r.Low = clamp01(p - z95*se)
	r.High = clamp01(p + z95*se)
	return r
}

// MeanCI 是带 95% 置信区间的均值（样本方差 → 标准误，n<2 时区间退化为点值）。
type MeanCI struct {
	N      int64   `json:"n"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Low    float64 `json:"ci_low"`
	High   float64 `json:"ci_high"`
}

// NewMeanCI 由和、平方和、样本数构造均值置信区间。
func NewMeanCI(sum, sumSquares float64, n int64) MeanCI {
	m := MeanCI{N: n}
	if n <= 0 {
		return m
	}
	mean := sum / float64(n)
	m.Mean = mean
	if n < 2 {
		m.Low, m.High = mean, mean
		return m
	}
	variance := (sumSquares - sum*sum/float64(n)) / float64(n-1)
	if variance < 0 { // 浮点误差保护
		variance = 0
	}
	m.StdDev = math.Sqrt(variance)
	margin := z95 * m.StdDev / math.Sqrt(float64(n))
	m.Low = math.Max(0, mean-margin)
	m.High = mean + margin
	return m
}

// DurationStat 是耗时分布（毫秒）。
type DurationStat struct {
	Count  int64   `json:"count"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	MaxMs  float64 `json:"max_ms"`
}

// NewDurationStat 由原始耗时样本（毫秒）构造统计；样本会被复制后排序。
func NewDurationStat(values []float64) DurationStat {
	stat := DurationStat{Count: int64(len(values))}
	if len(values) == 0 {
		return stat
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	stat.MeanMs = sum / float64(len(sorted))
	stat.P50Ms = percentileSorted(sorted, 0.50)
	stat.P95Ms = percentileSorted(sorted, 0.95)
	stat.MaxMs = sorted[len(sorted)-1]
	return stat
}

// percentileSorted 在已排序切片上取 p 分位（nearest-rank 法）。
func percentileSorted(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// SeatMetrics 是单个座位在一批评测上的指标。
type SeatMetrics struct {
	Seat   int    `json:"seat"`
	Agent  string `json:"agent"`
	Rounds int64  `json:"rounds"`
	Games  int64  `json:"games"`

	Wins      int64 `json:"wins"`
	TsumoWins int64 `json:"tsumo_wins"`
	RonWins   int64 `json:"ron_wins"`
	Deals     int64 `json:"deals"`
	Riichi    int64 `json:"riichi"`
	Melds     int64 `json:"melds"`
	Ankan     int64 `json:"ankan"`
	OpenWins  int64 `json:"open_wins"`
	Yakuman   int64 `json:"yakuman"`

	Decisions int64        `json:"decisions"`
	Decision  DurationStat `json:"decision_ms"`

	WinRate     Rate   `json:"win_rate"`
	TsumoRate   Rate   `json:"tsumo_rate"`
	DealInRate  Rate   `json:"deal_in_rate"`
	RiichiRate  Rate   `json:"riichi_rate"`
	MeldRate    Rate   `json:"meld_rate"`
	OpenWinRate Rate   `json:"open_win_rate"`
	AvgRank     MeanCI `json:"avg_rank"`
	AvgWinPts   MeanCI `json:"avg_win_points"`
}

// seatAccumulator 是单个座位的累加器（只在汇总阶段使用）。
type seatAccumulator struct {
	agent     string
	rounds    int64
	games     int64
	wins      int64
	tsumoWins int64
	ronWins   int64
	deals     int64
	riichi    int64
	melds     int64
	ankan     int64
	openWins  int64
	yakuman   int64

	decisions  int64
	winPoints  int64
	winPoints2 int64
	rankSum    float64
	rankSum2   float64
	latencies  []float64
}

// add 把一场对局里该座位的计数累加进来；rounds 是该场已打完的轮数。
func (a *seatAccumulator) add(st SeatRoundStats, latencies []float64, rounds int) {
	a.rounds += int64(rounds)
	a.wins += st.Wins
	a.tsumoWins += st.TsumoWins
	a.ronWins += st.RonWins
	a.deals += st.Deals
	a.riichi += st.Riichi
	a.melds += st.Melds
	a.ankan += st.Ankan
	a.openWins += st.OpenWins
	a.yakuman += st.Yakuman
	a.decisions += st.Decisions
	a.winPoints += st.WinPoints
	a.winPoints2 += st.WinPoints * st.WinPoints
	a.latencies = append(a.latencies, latencies...)
}

// addRank 记录一场有效对局的最终顺位。
func (a *seatAccumulator) addRank(rank int) {
	a.games++
	a.rankSum += float64(rank)
	a.rankSum2 += float64(rank * rank)
}

// finalize 生成对外指标。
func (a *seatAccumulator) finalize(seat int) SeatMetrics {
	return SeatMetrics{
		Seat:        seat,
		Agent:       a.agent,
		Rounds:      a.rounds,
		Games:       a.games,
		Wins:        a.wins,
		TsumoWins:   a.tsumoWins,
		RonWins:     a.ronWins,
		Deals:       a.deals,
		Riichi:      a.riichi,
		Melds:       a.melds,
		Ankan:       a.ankan,
		OpenWins:    a.openWins,
		Yakuman:     a.yakuman,
		Decisions:   a.decisions,
		Decision:    NewDurationStat(a.latencies),
		WinRate:     NewRate(a.wins, a.rounds),
		TsumoRate:   NewRate(a.tsumoWins, a.rounds),
		DealInRate:  NewRate(a.deals, a.rounds),
		RiichiRate:  NewRate(a.riichi, a.rounds),
		MeldRate:    NewRate(a.melds, a.rounds),
		OpenWinRate: NewRate(a.openWins, a.wins),
		AvgRank:     NewMeanCI(a.rankSum, a.rankSum2, a.games),
		AvgWinPts:   NewMeanCI(float64(a.winPoints), float64(a.winPoints2), a.wins),
	}
}

// summarize 汇总所有对局。
func summarize(cfg Config, games []GameResult) Summary {
	var accs [4]seatAccumulator
	for seat := range accs {
		accs[seat].agent = cfg.Agents[seat]
	}

	summary := Summary{Games: int64(len(games))}
	var durations []float64
	pairwise := NewPairwise(cfg.Agents)

	for _, g := range games {
		valid := g.Error == "" && !g.Aborted
		if valid {
			pairwise.AddScores(cfg.Agents, g.FinalScores)
		} else {
			summary.ErrorN++
		}
		summary.Rounds += int64(g.Rounds)
		durations = append(durations, g.DurationMs)
		for seat := 0; seat < 4; seat++ {
			accs[seat].add(g.Seats[seat], g.LatenciesMs[seat], g.Rounds)
			if valid {
				accs[seat].addRank(g.Ranks[seat])
			}
		}
	}
	for seat := range accs {
		summary.Seats[seat] = accs[seat].finalize(seat)
	}
	summary.Elo = pairwise.Ratings(200)
	summary.Duration = NewDurationStat(durations)
	return summary
}

// ranksOf 按最终点数给出 1..4 的顺位；同分并列（1 + 严格高于自己的座位数）。
func ranksOf(scores [4]int) [4]int {
	var ranks [4]int
	for seat := 0; seat < 4; seat++ {
		rank := 1
		for other := 0; other < 4; other++ {
			if other != seat && scores[other] > scores[seat] {
				rank++
			}
		}
		ranks[seat] = rank
	}
	return ranks
}

// winPayload 是和了事件里 eval 需要的字段（与 game.emitWinEvent 的 payload 对齐）。
type winPayload struct {
	Points int      `json:"points"`
	Han    int      `json:"han"`
	Yaku   []string `json:"yaku"`
	Riichi bool     `json:"riichi"`
	Tsumo  bool     `json:"tsumo"`
}

// callPayload 是吃/碰/杠事件里 eval 需要的字段。
type callPayload struct {
	MeldKind string `json:"meld_kind"`
	KanType  string `json:"kan_type"`
}

// tallyRound 把 state.Events[from:] 这一段（本局新增事件）计入 GameResult。
//
// 用事件而不是 state 快照统计，天然覆盖「一炮多响」「杠后立直棒」等边界情况。
func tallyRound(res *GameResult, state *game.State, from int) {
	lastDiscard := -1
	var meldCount [4]int // 本局各座位的副露/杠组数（用于识别副露和了）
	for _, event := range state.Events[from:] {
		switch event.Kind {
		case game.EventRoundStart:
			lastDiscard = -1
			meldCount = [4]int{}
		case game.EventDiscard:
			lastDiscard = int(event.Seat)
		case game.EventRiichi:
			res.Seats[event.Seat].Riichi++
		case game.EventChi, game.EventPon:
			res.Seats[event.Seat].Melds++
			meldCount[event.Seat]++
		case game.EventKan:
			var payload callPayload
			if len(event.Payload) > 0 {
				_ = json.Unmarshal(event.Payload, &payload)
			}
			if payload.MeldKind == string(game.MeldKanAnkan) {
				res.Seats[event.Seat].Ankan++
			} else {
				res.Seats[event.Seat].Melds++
			}
			meldCount[event.Seat]++
		case game.EventTsumo, game.EventRon:
			stats := &res.Seats[event.Seat]
			stats.Wins++
			var payload winPayload
			if len(event.Payload) > 0 {
				_ = json.Unmarshal(event.Payload, &payload)
			}
			stats.WinPoints += int64(payload.Points)
			if payload.Han >= 13 {
				stats.Yakuman++
			}
			// 有副露/杠的和了：计分走 1 翻 30 符近似（见 scorer.go），单独计数。
			if meldCount[event.Seat] > 0 {
				stats.OpenWins++
			}
			if event.Kind == game.EventTsumo {
				stats.TsumoWins++
			} else {
				stats.RonWins++
				if lastDiscard >= 0 && lastDiscard < 4 {
					res.Seats[lastDiscard].Deals++
				}
			}
		}
	}
}

// clamp01 把比例限制在 [0,1]。
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
