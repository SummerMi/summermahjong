package eval

import (
	"math"
	"sort"
)

// EloResult 是 Bradley-Terry 拟合出的相对实力评级（ELO 刻度，均值 1500）。
//
// 为什么用 Bradley-Terry 而不是逐局 ELO 更新：自对弈里四家同时在场，
// 「A 比 B 强多少」应该由两两胜负矩阵一次性拟合；BT 的 MM 迭代 200 轮内收敛，
// 对「某 agent 从未赢过」这类极端数据也比逐局更新稳定（配合 p 下限处理）。
type EloResult struct {
	Names      []string           `json:"names"`
	Ratings    map[string]float64 `json:"ratings"`
	Iterations int                `json:"iterations"`
	Converged  bool               `json:"converged"`
	Matches    int                `json:"matches"` // 两两比较场次（并列按 0.5/0.5 计入）
}

// Pairwise 是 agent 两两胜负矩阵，按 **agent 名字** 聚合（同名坐多席时合并）。
type Pairwise struct {
	names []string
	index map[string]int
	wins  [][]float64 // wins[i][j]：i 赢 j 的次数（并列各记 0.5）
	games [][]float64 // games[i][j]：i 与 j 的有效比较次数
}

// NewPairwise 按 agent 名单构造空矩阵（顺序去重，保证输出可复现）。
func NewPairwise(agents []string) *Pairwise {
	p := &Pairwise{index: make(map[string]int, len(agents))}
	for _, name := range agents {
		if _, ok := p.index[name]; ok {
			continue
		}
		p.index[name] = len(p.names)
		p.names = append(p.names, name)
	}
	n := len(p.names)
	p.wins = make([][]float64, n)
	p.games = make([][]float64, n)
	for i := 0; i < n; i++ {
		p.wins[i] = make([]float64, n)
		p.games[i] = make([]float64, n)
	}
	return p
}

// Names 返回去重后的 agent 名字（稳定顺序）。
func (p *Pairwise) Names() []string {
	out := make([]string, len(p.names))
	copy(out, p.names)
	return out
}

// AddScores 用一场对局的最终点数更新两两胜负：点数高者赢，平分则各记 0.5。
func (p *Pairwise) AddScores(agents []string, scores [4]int) {
	for i := 0; i < len(agents); i++ {
		for j := i + 1; j < len(agents); j++ {
			hi, ok := p.index[agents[i]]
			if !ok {
				continue
			}
			hj, ok := p.index[agents[j]]
			if !ok {
				continue
			}
			if hi == hj { // 同名对打，跳过（自己打自己无意义）
				continue
			}
			switch {
			case scores[i] > scores[j]:
				p.wins[hi][hj]++
			case scores[i] < scores[j]:
				p.wins[hj][hi]++
			default:
				p.wins[hi][hj] += 0.5
				p.wins[hj][hi] += 0.5
			}
			p.games[hi][hj]++
			p.games[hj][hi]++
		}
	}
}

// Wins 返回 i 对 j 的胜场（含并列的 0.5）；测试与报告用。
func (p *Pairwise) Wins(i, j int) float64 { return p.wins[i][j] }

// Games 返回 i 与 j 的有效比较次数。
func (p *Pairwise) Games(i, j int) float64 { return p.games[i][j] }

// Matches 返回两两比较总场次。
func (p *Pairwise) Matches() int {
	total := 0
	for i := range p.names {
		for j := i + 1; j < len(p.names); j++ {
			total += int(p.games[i][j])
		}
	}
	return total
}

// Ratings 用 MM（Minorization-Maximization）算法拟合 Bradley-Terry 强度，迭代最多 iterations 轮。
//
// 模型：P(i 胜 j) = p_i / (p_i + p_j)，对数似然最大化的 MM 更新为
//
//	p_i' = W_i / Σ_j ( N_ij / (p_i + p_j) )
//
// 其中 W_i 是 i 的总胜场、N_ij 是 i-j 比较次数。数值保护：p 下限 1e-9、
// 每轮按几何均值归一，最后线性平移到均值 1500 的 ELO 刻度。
func (p *Pairwise) Ratings(iterations int) EloResult {
	n := len(p.names)
	result := EloResult{Names: p.Names(), Ratings: make(map[string]float64, n), Matches: p.Matches()}
	if iterations <= 0 {
		iterations = 200
	}
	if n == 0 {
		result.Converged = true
		return result
	}
	if n == 1 {
		result.Ratings[p.names[0]] = 1500
		result.Iterations = 1
		result.Converged = true
		return result
	}

	totals := make([]float64, n)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			totals[i] += p.wins[i][j]
		}
	}
	pv := make([]float64, n)
	for i := range pv {
		pv[i] = 1.0 // 几何均值为 1 的初值
	}

	converged := false
	iterationsDone := 0
	for iteration := 0; iteration < iterations; iteration++ {
		next := make([]float64, n)
		maxDelta := 0.0
		for i := 0; i < n; i++ {
			denom := 0.0
			for j := 0; j < n; j++ {
				if i == j || p.games[i][j] == 0 {
					continue
				}
				denom += p.games[i][j] / (pv[i] + pv[j])
			}
			if denom <= 0 {
				next[i] = pv[i]
				continue
			}
			next[i] = totals[i] / denom
			if next[i] < 1e-9 {
				next[i] = 1e-9
			}
			delta := math.Abs(next[i]-pv[i]) / pv[i]
			if delta > maxDelta {
				maxDelta = delta
			}
		}
		// 几何均值归一到 1，避免整体尺度漂移。
		logSum := 0.0
		for _, v := range next {
			logSum += math.Log(v)
		}
		scale := math.Exp(logSum / float64(n))
		if scale > 0 && !math.IsInf(scale, 0) {
			for i := range next {
				next[i] /= scale
			}
		}
		pv = next
		iterationsDone = iteration + 1
		if maxDelta < 1e-10 {
			converged = true
			break
		}
	}

	// 转 ELO 刻度：400/ln10 是 ELO 与自然对数胜率比的标准换算系数。
	ratings := make([]float64, n)
	mean := 0.0
	for i, v := range pv {
		rating := 400 / math.Ln10 * math.Log(v)
		if math.IsNaN(rating) || math.IsInf(rating, 0) {
			rating = 0 // 数值兜底：绝不让 NaN/Inf 进入 ratings
		}
		ratings[i] = rating
		mean += rating
	}
	mean /= float64(n)
	for i, name := range p.names {
		result.Ratings[name] = math.Round((ratings[i]-mean+1500)*10) / 10
	}
	result.Iterations = iterationsDone
	result.Converged = converged
	return result
}

// Rating 是排序后的一条 ELO 记录。
type Rating struct {
	Name   string  `json:"name"`
	Rating float64 `json:"rating"`
}

// SortedRatings 按评分降序返回，便于报告表格稳定排序（同分按名字）。
func (e EloResult) SortedRatings() []Rating {
	entries := make([]Rating, 0, len(e.Ratings))
	for name, rating := range e.Ratings {
		entries = append(entries, Rating{Name: name, Rating: rating})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Rating != entries[j].Rating {
			return entries[i].Rating > entries[j].Rating
		}
		return entries[i].Name < entries[j].Name
	})
	return entries
}
