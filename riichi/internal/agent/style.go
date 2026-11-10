package agent

import (
	"strings"

	"summermahjong/riichi/internal/tile"
)

// Style 是一种「打风」：同一套决策算法，只调权重与倾向参数。
//
// 这样四类策略（向听 / 防守 / 搜索）共用同一份估值代码，S9 评测只需按名字
// （如 "shanten:attack"）换 Style，就能对比不同打风的胜率/放铳率。
//
// 权重含义（评分越低越好）：
//
//	score = ShantenWeight*向听数 - UkeireWeight*有效进张 - KeepValueWeight*牌价值 + DangerWeight*危险度
//
// 其中向听数与危险度的数量级已在各预设里调好，保证「打风差异」可解释：
//   - attack（速攻）：向听/进张优先，危险度几乎不看；断幺倾向强，先切幺九字牌；
//   - closed（門前）：更看重进张（两面/嵌张改良），危险度略看，保留门清做牌价值；
//   - defense（守備）：危险度权重压过向听，立直宣言后优先现物/筋牌。
//
// MeldsBias / RiichiBias 预留给 M2 的吃碰/立直动作：M1 只有摸打，暂不参与决策，
// 但接口先占位，避免以后再改 Style 结构（契约允许新增字段）。
type Style struct {
	Name string

	ShantenWeight   float64
	UkeireWeight    float64
	KeepValueWeight float64
	DangerWeight    float64

	// TanyaoBias 是断幺倾向：越大越愿意先切幺九/字牌，保留 2-8 中张。
	TanyaoBias float64
	// MeldsBias 是副露倾向（M2 吃碰杠使用；M1 未使用）。
	MeldsBias float64
	// RiichiBias 是立直倾向（M2 立直使用；M1 未使用）。
	RiichiBias float64
}

// 三种打风预设。它们是只读值，调用方需要微调时复制一份再改。
var (
	// StyleAttack 是速攻：效率优先、几乎不顾安全，断幺倾向强。
	StyleAttack = Style{
		Name:            "attack",
		ShantenWeight:   100,
		UkeireWeight:    0.9,
		KeepValueWeight: 1.2,
		DangerWeight:    0.05,
		TanyaoBias:      1.0,
		MeldsBias:       0.8,
		RiichiBias:      0.2,
	}
	// StyleClosed 是門前：重视进张与门清做牌价值，危险度轻度参与。
	StyleClosed = Style{
		Name:            "closed",
		ShantenWeight:   100,
		UkeireWeight:    1.3,
		KeepValueWeight: 0.5,
		DangerWeight:    0.4,
		TanyaoBias:      0.4,
		MeldsBias:       0.1,
		RiichiBias:      0.8,
	}
	// StyleDefense 是守備：危险度权重远高于向听，立直后先保命。
	StyleDefense = Style{
		Name:            "defense",
		ShantenWeight:   100,
		UkeireWeight:    0.5,
		KeepValueWeight: 0.1,
		DangerWeight:    12,
		TanyaoBias:      0,
		MeldsBias:       0,
		RiichiBias:      0.2,
	}
)

// StyleByName 按名字查打风（"attack" / "closed" / "defense"）。
func StyleByName(name string) (Style, bool) {
	switch strings.ToLower(name) {
	case StyleAttack.Name:
		return StyleAttack, true
	case StyleClosed.Name:
		return StyleClosed, true
	case StyleDefense.Name:
		return StyleDefense, true
	default:
		return Style{}, false
	}
}

// keepValue 返回一张牌在「保留价值」上的粗打分（0..8）：越高越该留在手里。
//
// 这不是役种判定，只是给候选切牌做同分时的启发式：
//   - 中张比幺九/字牌更适合做两面与断幺，保留价值更高；
//   - TanyaoBias 越大，幺九/字牌的保留价值越低（速攻先切它们）。
func keepValue(kind tile.Kind, style Style) float64 {
	base := 5.0
	switch {
	case kind.IsHonor():
		base = 2
	case kind.IsTerminal():
		base = 2.5
	}
	if !kind.IsTerminalOrHonor() {
		base += 3 * style.TanyaoBias
	}
	// 三元牌/风牌成对时有役种价值（役牌），略抬高一点，避免速攻无脑切光。
	if kind.IsDragon() || kind.IsWind() {
		base += 1
	}
	return base
}

// maxKeepValue 是 keepValue 的上界，用于把「切掉低价值牌」换算成得分。
const maxKeepValue = 8.0
