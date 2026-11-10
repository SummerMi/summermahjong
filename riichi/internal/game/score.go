package game

import "summermahjong/riichi/internal/tile"

// ScoreProvider 是和牌计分的注入点。
//
// 为什么抽象成接口而不是直接调用规则包：S4（役种/符/点）与本切片并行开发，
// 引擎先把「流程」写完（S5），计分用占位实现跑通；S4 合并后只需注入
// rules.Evaluate/Score 的适配器，状态机、事件与测试都不用改。
//
// 契约约定（v1.2）：Evaluate 返回的 han **不含宝牌**，由计分实现自己根据
// WinContext.DoraIndicators / UraIndicators / 手牌赤宝牌算 DoraCount，
// 最终调用 Score(han+DoraCount, fu, ctx)。本接口把这件事整体交给 Provider。
type ScoreProvider interface {
	// ScoreWin 计算一次和牌结果（含符/翻/役与四家点数变化）。
	// 返回的 Deltas 必须满足四家之和为 0（本场、放铳/自摸分摊都在内部算好）；
	// 立直棒由引擎在结算时加给首个和了者，Provider 不需要处理。
	ScoreWin(ctx WinContext) (WinResult, error)
}

// WinContext 是计分所需的全部输入（纯数据，不含 State 指针，便于替换实现与测试）。
type WinContext struct {
	Seat       Seat // 和了者
	DealerSeat Seat // 亲家座位（自摸分摊与亲家倍数需要）
	RonTarget  Seat // 放铳者（自摸时无意义，可忽略）
	Tsumo      bool // 自摸
	Dealer     bool // 和了者是否亲家
	RoundWind  Wind // 场风
	SeatWind   Wind // 自风

	Hand        []tile.Tile // 和牌后的门清手牌（含和了牌），张数 = 14 - 3*副露数
	Melds       []Meld      // 副露
	WinningTile tile.Tile   // 和了牌

	Riichi  bool // 立直
	Ippatsu bool // 一发
	Rinshan bool // 岭上开花
	Chankan bool // 抢杠
	Haitei  bool // 海底摸月（自摸最后一张）
	Houtei  bool // 河底捞鱼（荣和最后一张打牌）
	Tenhou  bool // 天和（预留）
	Chiihou bool // 地和（预留）

	DoraIndicators []tile.Tile // 表宝牌指示牌
	UraIndicators  []tile.Tile // 裏宝牌指示牌（立直和了时有效）

	Honba        int // 本场
	RiichiSticks int // 场上立直棒（引擎另算，Provider 可忽略）
}

// WinResult 是一次和牌的结算结果。
type WinResult struct {
	Han    int      // 总翻数（含宝牌，不含本场/立直棒点数）
	Fu     int      // 符
	Yaku   []string // 成立役种名（用于事件展示）
	Deltas [4]int   // 四家净点数变化（含本场；立直棒由引擎加）
}

// placeholderScorer 是 S4 合并前的占位计分：固定 1 翻 30 符。
//
// 点数算法与正式规则一致（基本点 = 符 × 2^(2+翻)，满贯封顶），因此连庄、本场、
// 自摸/荣和分摊、トビ判定等流程测试在 S4 合并前就能稳定跑通。
type placeholderScorer struct{}

// ScoreWin 实现 ScoreProvider：1 翻 30 符 + 本场。
func (placeholderScorer) ScoreWin(ctx WinContext) (WinResult, error) {
	const (
		han = 1
		fu  = 30
	)
	basic := basicPoints(han, fu)
	deltas := [4]int{}

	if ctx.Tsumo {
		if ctx.Dealer {
			// 亲家自摸：每家支付 basic*2，切上 100
			each := ceil100(basic * 2)
			for seat := 0; seat < 4; seat++ {
				if Seat(seat) == ctx.Seat {
					continue
				}
				deltas[seat] -= each
				deltas[ctx.Seat] += each
			}
		} else {
			for seat := 0; seat < 4; seat++ {
				if Seat(seat) == ctx.Seat {
					continue
				}
				pay := ceil100(basic)
				if Seat(seat) == ctx.DealerSeat {
					pay = ceil100(basic * 2)
				}
				deltas[seat] -= pay
				deltas[ctx.Seat] += pay
			}
		}
		for seat := 0; seat < 4; seat++ {
			if Seat(seat) == ctx.Seat {
				continue
			}
			deltas[seat] -= 100 * ctx.Honba
			deltas[ctx.Seat] += 100 * ctx.Honba
		}
	} else {
		pay := ceil100(basic * 4)
		if ctx.Dealer {
			pay = ceil100(basic * 6)
		}
		deltas[ctx.RonTarget] -= pay
		deltas[ctx.Seat] += pay
		deltas[ctx.RonTarget] -= 300 * ctx.Honba
		deltas[ctx.Seat] += 300 * ctx.Honba
	}

	return WinResult{
		Han:    han,
		Fu:     fu,
		Yaku:   []string{"和了（占位计分）"},
		Deltas: deltas,
	}, nil
}

// basicPoints 计算基本点（符 × 2^(2+翻)），并应用满贯以上封顶。
func basicPoints(han, fu int) int {
	if han >= 13 {
		return 8000
	}
	if han >= 11 {
		return 6000
	}
	if han >= 8 {
		return 4000
	}
	if han >= 6 {
		return 3000
	}
	if han >= 5 {
		return 2000
	}
	return fu << uint(2+han)
}

// ceil100 向上取整到 100 点。
func ceil100(points int) int {
	if points <= 0 {
		return 0
	}
	return (points + 99) / 100 * 100
}
