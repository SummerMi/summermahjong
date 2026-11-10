package tile

import "math/bits"

// SplitMix64 是一个确定性、跨平台、跨 Go 版本的伪随机数生成器。
//
// 为什么不用 math/rand：标准库的 Shuffle 实现细节可能在版本间变化，
// 而牌谱复现要求「同一 seed 在任何机器、任何版本上得到同一副牌」。
// SplitMix64 只有加法和位运算，实现固定，天然满足这个要求。
type SplitMix64 struct {
	state uint64
}

// NewSplitMix64 用 seed 初始化生成器（任意 int64）。
func NewSplitMix64(seed int64) *SplitMix64 {
	return &SplitMix64{state: uint64(seed)}
}

// Next 返回下一个 64 位随机数。
func (r *SplitMix64) Next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Intn 返回 [0, n) 内均匀分布的整数；n <= 0 时返回 0。
//
// 用拒绝采样消除取模偏差，保证牌山分布均匀。
func (r *SplitMix64) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	un := uint64(n)
	limit := ^uint64(0) - (^uint64(0) % un)
	for {
		v := r.Next()
		if v < limit {
			return int(v % un)
		}
	}
}

// Shuffle 用 Fisher-Yates 原地洗牌，结果由 seed 唯一确定。
func Shuffle(deck []Tile, seed int64) {
	rng := NewSplitMix64(seed)
	for i := len(deck) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		deck[i], deck[j] = deck[j], deck[i]
	}
}

// popcount 用于诊断（牌山统计），保留以便测试与调试。
func popcount(x uint64) int { return bits.OnesCount64(x) }
