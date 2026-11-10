package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// tool 是一个 MCP 工具：声明（name/description/inputSchema）+ 纯函数 handler。
type tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(context.Context, json.RawMessage) (any, error)
}

// defaultTools 返回注册的工具列表；顺序固定，保证 tools/list 输出可复现。
func defaultTools() []tool {
	return []tool{
		shantenTool(),
		ukeireTool(),
		safetyTool(),
		scoreTool(),
		yakuPotentialTool(),
	}
}

// ── mahjong.shanten ──────────────────────────────────────────────────────

type shantenArgs struct {
	Hand      []string `json:"hand"`
	MeldCount *int     `json:"meldCount"`
}

type shantenResult struct {
	Hand      []string `json:"hand"`
	MeldCount int      `json:"meld_count"`
	Shanten   int      `json:"shanten"`
	Tenpai    bool     `json:"tenpai"`
}

func shantenTool() tool {
	return tool{
		Name:        "mahjong.shanten",
		Description: "计算手牌向听数：0=听牌，-1=已和牌形，n=n 向听。hand 张数须等于 13-3*meldCount。",
		InputSchema: schemaObject(map[string]any{
			"hand":      schemaStringArray("手牌，如 [\"1m\",\"2m\",\"3m\",...]；听牌形为 13-3*meldCount 张"),
			"meldCount": schemaInteger("已副露面子数（0..4，默认 0）", intPtr(0), intPtr(4)),
		}, "hand"),
		Handler: handleShanten,
	}
}

func handleShanten(_ context.Context, args json.RawMessage) (any, error) {
	var in shantenArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, invalidParams("参数格式错误: %v", err)
	}
	hand, err := parseHandArg(in.Hand, "hand")
	if err != nil {
		return nil, err
	}
	meldCount, err := meldCountArg(in.MeldCount, len(hand))
	if err != nil {
		return nil, err
	}
	shanten := rules.Shanten(hand, meldCount)
	if shanten == rules.ShantenInvalid {
		return nil, invalidParams("手牌无法计算向听（张数或牌面非法）")
	}
	return shantenResult{
		Hand:      tilesToStrings(hand),
		MeldCount: meldCount,
		Shanten:   shanten,
		Tenpai:    shanten <= 0,
	}, nil
}

// ── mahjong.ukeire ───────────────────────────────────────────────────────

type ukeireArgs struct {
	Hand      []string `json:"hand"`
	MeldCount *int     `json:"meldCount"`
}

type ukeireTile struct {
	Tile         string `json:"tile"`
	Remaining    int    `json:"remaining"`
	DiscardAfter string `json:"discard_after,omitempty"`
	Win          bool   `json:"win,omitempty"`
}

type ukeireResult struct {
	Hand      []string     `json:"hand"`
	MeldCount int          `json:"meld_count"`
	Shanten   int          `json:"shanten"`
	Tiles     []ukeireTile `json:"tiles"`
	Total     int          `json:"total"`
}

func ukeireTool() tool {
	return tool{
		Name:        "mahjong.ukeire",
		Description: "计算有效进张：摸到后能降低向听的牌种、各自剩余枚数与摸到后打哪张最优。",
		InputSchema: schemaObject(map[string]any{
			"hand":      schemaStringArray("手牌，张数须等于 13-3*meldCount"),
			"meldCount": schemaInteger("已副露面子数（0..4，默认 0）", intPtr(0), intPtr(4)),
		}, "hand"),
		Handler: handleUkeire,
	}
}

func handleUkeire(_ context.Context, args json.RawMessage) (any, error) {
	var in ukeireArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, invalidParams("参数格式错误: %v", err)
	}
	hand, err := parseHandArg(in.Hand, "hand")
	if err != nil {
		return nil, err
	}
	meldCount, err := meldCountArg(in.MeldCount, len(hand))
	if err != nil {
		return nil, err
	}
	base := rules.Shanten(hand, meldCount)
	if base == rules.ShantenInvalid {
		return nil, invalidParams("手牌无法计算向听（张数或牌面非法）")
	}

	visible := tileCounts(hand)
	tiles := make([]ukeireTile, 0, len(hand))
	total := 0
	for kind := tile.Kind(0); kind < tile.NumKinds; kind++ {
		remaining := remainingCopies(visible, kind)
		if remaining == 0 {
			continue
		}
		after := make([]tile.Tile, 0, len(hand)+1)
		after = append(after, hand...)
		after = append(after, tile.Tile{Kind: kind})

		// 门清听牌形：摸到和了牌本身就是「有效进张」（向听不会从 0 再降，
		// 所以不能只看 rules.Shanten 的下降）。
		if meldCount == 0 && len(after) == 14 && isWinningTiles(after) {
			tiles = append(tiles, ukeireTile{Tile: kind.String(), Remaining: remaining, Win: true})
			total += remaining
			continue
		}

		bestShanten := rules.ShantenInvalid
		bestDiscard := ""
		for i := range after {
			s := rules.Shanten(removeTileAt(after, i), meldCount)
			if s == rules.ShantenInvalid {
				continue
			}
			if bestShanten == rules.ShantenInvalid || s < bestShanten {
				bestShanten = s
				bestDiscard = after[i].String()
			}
		}
		if bestShanten == rules.ShantenInvalid || bestShanten >= base {
			continue
		}
		tiles = append(tiles, ukeireTile{Tile: kind.String(), Remaining: remaining, DiscardAfter: bestDiscard})
		total += remaining
	}
	return ukeireResult{Hand: tilesToStrings(hand), MeldCount: meldCount, Shanten: base, Tiles: tiles, Total: total}, nil
}

// isWinningTiles 报告一组 14 张门清手牌是否已经和牌（换一张和了牌逐个拆解即可）。
//
// rules.Decompose 要求「hand 含 winning」，所以这里对出现过的每种牌都试一次拆解；
// 只要有一种成立，就说明这手牌已经是和牌形。
func isWinningTiles(hand14 []tile.Tile) bool {
	seen := make(map[tile.Kind]struct{}, len(hand14))
	for _, t := range hand14 {
		if _, ok := seen[t.Kind]; ok {
			continue
		}
		seen[t.Kind] = struct{}{}
		if _, ok := rules.Decompose(hand14, nil, t); ok {
			return true
		}
	}
	return false
}

// ── mahjong.safety ───────────────────────────────────────────────────────

type safetyArgs struct {
	Hand     []string   `json:"hand"`
	Discards [][]string `json:"discards"`
	Riichi   []bool     `json:"riichi"`
	Seat     *int       `json:"seat"`
}

type safetyCandidate struct {
	Tile    string   `json:"tile"`
	Danger  float64  `json:"danger"`
	Reasons []string `json:"reasons"`
}

type safetyResult struct {
	Seat        int               `json:"seat"`
	RiichiSeats []int             `json:"riichi_seats"`
	Candidates  []safetyCandidate `json:"candidates"`
	Note        string            `json:"note,omitempty"`
}

func safetyTool() tool {
	return tool{
		Name: "mahjong.safety",
		Description: "候选牌危险度排序：对已立直对手估算放铳危险度（0 最安全，越大越危险），" +
			"考虑现物 / 筋 / 壁 / 剩余张数；返回按危险度升序的候选。",
		InputSchema: schemaObject(map[string]any{
			"hand": schemaStringArray("自己的手牌（通常 13/14 张）"),
			"discards": map[string]any{
				"type":        "array",
				"maxItems":    4,
				"description": "四家舍牌，下标即座位（0..3）；可省略",
				"items":       schemaStringArray("某一家舍牌"),
			},
			"riichi": map[string]any{
				"type":        "array",
				"maxItems":    4,
				"items":       map[string]any{"type": "boolean"},
				"description": "四家立直状态，下标即座位；可省略",
			},
			"seat": schemaInteger("自己的座位 0..3（默认 0）", intPtr(0), intPtr(3)),
		}, "hand"),
		Handler: handleSafety,
	}
}

func handleSafety(_ context.Context, args json.RawMessage) (any, error) {
	var in safetyArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, invalidParams("参数格式错误: %v", err)
	}
	hand, err := parseHandArg(in.Hand, "hand")
	if err != nil {
		return nil, err
	}
	seat := 0
	if in.Seat != nil {
		seat = *in.Seat
	}
	if seat < 0 || seat > 3 {
		return nil, invalidParams("seat 必须在 0..3 之间")
	}
	if len(in.Riichi) > 4 {
		return nil, invalidParams("riichi 数组最多 4 个元素")
	}
	if len(in.Discards) > 4 {
		return nil, invalidParams("discards 最多 4 家")
	}

	var discards [4][]tile.Tile
	for i, names := range in.Discards {
		tiles, err := parseTilesAllowEmpty(names, fmt.Sprintf("discards[%d]", i))
		if err != nil {
			return nil, err
		}
		discards[i] = tiles
	}
	var riichi [4]bool
	for i := 0; i < len(in.Riichi) && i < 4; i++ {
		riichi[i] = in.Riichi[i]
	}

	visible := tileCounts(hand)
	for seatIdx := 0; seatIdx < 4; seatIdx++ {
		for _, t := range discards[seatIdx] {
			if t.Kind < tile.NumKinds {
				visible[t.Kind]++
			}
		}
	}

	riichiSeats := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		if riichi[i] && i != seat {
			riichiSeats = append(riichiSeats, i)
		}
	}

	candidates := make([]safetyCandidate, 0, len(hand))
	for _, kind := range sortedKinds(hand) {
		cand := safetyCandidate{Tile: kind.String(), Danger: 0, Reasons: nil}
		if len(riichiSeats) == 0 {
			cand.Reasons = append(cand.Reasons, "无人立直：按效率优先")
		} else {
			total := 0.0
			for _, opp := range riichiSeats {
				danger, reasons := dangerForKind(discards[opp], kind, visible)
				total += danger
				if len(reasons) > 0 {
					cand.Reasons = append(cand.Reasons, fmt.Sprintf("对 seat%d：%s", opp, strings.Join(reasons, "、")))
				}
			}
			cand.Danger = round1(total / float64(len(riichiSeats)))
		}
		candidates = append(candidates, cand)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Danger != candidates[j].Danger {
			return candidates[i].Danger < candidates[j].Danger
		}
		return candidates[i].Tile < candidates[j].Tile
	})

	out := safetyResult{Seat: seat, RiichiSeats: riichiSeats, Candidates: candidates}
	if len(riichiSeats) == 0 {
		out.Note = "当前没有对手立直；危险度均为 0，排序不改变。"
	}
	return out, nil
}

// ── mahjong.score ────────────────────────────────────────────────────────

type scoreArgs struct {
	Hand    []string      `json:"hand"`
	Winning string        `json:"winning"`
	Melds   []meldArgs    `json:"melds"`
	Ctx     *scoreCtxArgs `json:"ctx"`
}

// meldArgs 是副露参数：kind 取 chi/pon/kan_daiminkan/kan_shouminkan/kan_ankan。
//
// 明暗由 kind 决定（暗杠 kan_ankan 不破坏门清），不需要调用方额外传 open。
type meldArgs struct {
	Kind  string   `json:"kind"`
	Tiles []string `json:"tiles"`
}

type scoreCtxArgs struct {
	Menzen         *bool    `json:"menzen"`
	Riichi         bool     `json:"riichi"`
	Ippatsu        bool     `json:"ippatsu"`
	Tsumo          bool     `json:"tsumo"`
	Rinshan        bool     `json:"rinshan"`
	Chankan        bool     `json:"chankan"`
	Haitei         bool     `json:"haitei"`
	Houtei         bool     `json:"houtei"`
	SeatWind       string   `json:"seat_wind"`
	RoundWind      string   `json:"round_wind"`
	Honba          int      `json:"honba"`
	RiichiSticks   int      `json:"riichi_sticks"`
	DoraIndicators []string `json:"dora_indicators"`
}

type yakuOut struct {
	Name    string `json:"name"`
	Han     int    `json:"han"`
	Yakuman bool   `json:"yakuman,omitempty"`
}

type pointsOut struct {
	Han         int `json:"han"`
	Fu          int `json:"fu"`
	Base        int `json:"base"`
	Ron         int `json:"ron"`
	TsumoChild  int `json:"tsumo_child"`
	TsumoDealer int `json:"tsumo_dealer"`
	Total       int `json:"total"`
}

type scoreResult struct {
	Hand      []string  `json:"hand"`
	Winning   string    `json:"winning"`
	MeldCount int       `json:"meld_count"`
	Pattern   string    `json:"pattern"`
	Wait      string    `json:"wait"`
	Jantou    string    `json:"jantou"`
	Yaku      []yakuOut `json:"yaku"`
	Han       int       `json:"han"`
	Fu        int       `json:"fu"`
	Dora      int       `json:"dora"`
	TotalHan  int       `json:"total_han"`
	HasYaku   bool      `json:"has_yaku"`
	Points    pointsOut `json:"points"`
}

func scoreTool() tool {
	return tool{
		Name: "mahjong.score",
		Description: "判定和牌并计算役种 / 翻 / 符 / 点数（门清或副露）。hand 只含暗牌：13-3*meldCount 张" +
			"（不含 winning，自动补上）或 14-3*meldCount 张（含 winning）。副露通过 melds 传 " +
			"kind=chi/pon/kan_* + tiles；宝牌按 ctx.dora_indicators 与赤 5 自动统计。",
		InputSchema: schemaObject(map[string]any{
			"hand":    schemaStringArray("暗牌：13-3*meldCount 张（不含 winning）或 14-3*meldCount 张（含 winning）"),
			"winning": map[string]any{"type": "string", "description": "和了牌，如 \"4p\" / \"r5m\""},
			"melds": map[string]any{
				"type":        "array",
				"maxItems":    4,
				"description": "已副露的面子（可省略 = 门清）",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"kind": map[string]any{
							"type": "string",
							"enum": []string{"chi", "pon", "kan_daiminkan", "kan_shouminkan", "kan_ankan"},
						},
						"tiles": schemaStringArray("该副露的牌：chi/pon 3 张，杠 4 张"),
					},
					"required": []string{"kind", "tiles"},
				},
			},
			"ctx": map[string]any{
				"type":                 "object",
				"description":          "和了场况（可省略；menzen 默认按副露自动推导）",
				"additionalProperties": false,
				"properties": map[string]any{
					"menzen":          map[string]any{"type": "boolean", "description": "门清；省略时按 melds 自动推导，显式传错会被拒绝"},
					"riichi":          map[string]any{"type": "boolean"},
					"ippatsu":         map[string]any{"type": "boolean"},
					"tsumo":           map[string]any{"type": "boolean"},
					"rinshan":         map[string]any{"type": "boolean"},
					"chankan":         map[string]any{"type": "boolean"},
					"haitei":          map[string]any{"type": "boolean"},
					"houtei":          map[string]any{"type": "boolean"},
					"seat_wind":       map[string]any{"type": "string", "description": "自风：E/S/W/N，默认 E（亲家）"},
					"round_wind":      map[string]any{"type": "string", "description": "场风：E/S/W/N，默认 E"},
					"honba":           map[string]any{"type": "integer", "minimum": 0},
					"riichi_sticks":   map[string]any{"type": "integer", "minimum": 0},
					"dora_indicators": schemaStringArray("表宝牌指示牌"),
				},
			},
		}, "hand", "winning"),
		Handler: handleScore,
	}
}

func handleScore(_ context.Context, args json.RawMessage) (any, error) {
	var in scoreArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, invalidParams("参数格式错误: %v", err)
	}
	hand, err := parseHandArg(in.Hand, "hand")
	if err != nil {
		return nil, err
	}
	winning, err := tile.ParseTile(in.Winning)
	if err != nil {
		return nil, invalidParams("winning=%q 不是合法牌面", in.Winning)
	}
	calledMelds, err := parseMeldsArg(in.Melds)
	if err != nil {
		return nil, err
	}
	meldCount := len(calledMelds)

	// hand 只含暗牌：不含 winning 时是 13-3*meldCount 张，含 winning 时是 14-3*meldCount 张。
	withoutWinning := 13 - 3*meldCount
	var concealed []tile.Tile
	switch len(hand) {
	case withoutWinning:
		concealed = append(concealed, hand...)
		concealed = append(concealed, winning)
	case withoutWinning + 1:
		if !containsKind(hand, winning.Kind) {
			return nil, invalidParams("含 winning 的 hand 里必须出现 winning=%q", in.Winning)
		}
		concealed = append(concealed, hand...)
	default:
		return nil, invalidParams(
			"hand 张数应为 %d（不含 winning）或 %d（含 winning），当前 %d 张（meldCount=%d）",
			withoutWinning, withoutWinning+1, len(hand), meldCount,
		)
	}
	if err := validateTileCounts(concealed); err != nil {
		return nil, err
	}

	shape, ok := rules.Decompose(concealed, calledMelds, winning)
	if !ok {
		return nil, invalidParams("不是和牌形（或副露 / 牌面非法 / 未包含 winning）")
	}

	// 门清 = 没有明面副露（暗杠不破坏门清）；调用方显式传 menzen 时以显式值为准，
	// 但与实际副露矛盾时 Evaluate 会返回 ErrInvalidContext（下面转成参数错误）。
	menzen := true
	for _, meld := range calledMelds {
		if meld.Kind != rules.MeldKanAnkan {
			menzen = false
			break
		}
	}
	ctx := rules.ScoreContext{Menzen: menzen, SeatWind: tile.East, RoundWind: tile.East}
	if in.Ctx != nil {
		if in.Ctx.Menzen != nil {
			ctx.Menzen = *in.Ctx.Menzen
		}
		ctx.Riichi = in.Ctx.Riichi
		ctx.Ippatsu = in.Ctx.Ippatsu
		ctx.Tsumo = in.Ctx.Tsumo
		ctx.Rinshan = in.Ctx.Rinshan
		ctx.Chankan = in.Ctx.Chankan
		ctx.Haitei = in.Ctx.Haitei
		ctx.Houtei = in.Ctx.Houtei
		ctx.Honba = in.Ctx.Honba
		ctx.RiichiSticks = in.Ctx.RiichiSticks
		if in.Ctx.Honba < 0 || in.Ctx.RiichiSticks < 0 {
			return nil, invalidParams("honba / riichi_sticks 不能为负")
		}
		seatWind, err := parseWind(in.Ctx.SeatWind)
		if err != nil {
			return nil, err
		}
		roundWind, err := parseWind(in.Ctx.RoundWind)
		if err != nil {
			return nil, err
		}
		ctx.SeatWind, ctx.RoundWind = seatWind, roundWind
		indicators, err := parseTilesAllowEmpty(in.Ctx.DoraIndicators, "ctx.dora_indicators")
		if err != nil {
			return nil, err
		}
		ctx.DoraIndicators = indicators
	}

	yaku, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		return nil, invalidParams("和牌场况不自洽: %v", err)
	}
	ctx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, ctx)
	points := rules.Score(han+dora, fu, ctx)

	yakuOuts := make([]yakuOut, 0, len(yaku))
	for _, y := range yaku {
		yakuOuts = append(yakuOuts, yakuOut{Name: y.Name, Han: y.Han, Yakuman: y.Yakuman})
	}
	return scoreResult{
		Hand:      tilesToStrings(hand),
		Winning:   winning.String(),
		MeldCount: meldCount,
		Pattern:   shape.Pattern,
		Wait:      string(shape.Wait),
		Jantou:    shape.Jantou.String(),
		Yaku:      yakuOuts,
		Han:       han,
		Fu:        fu,
		Dora:      dora,
		TotalHan:  han + dora,
		HasYaku:   len(yaku) > 0,
		Points: pointsOut{
			Han: points.Han, Fu: points.Fu, Base: points.Base, Ron: points.Ron,
			TsumoChild: points.TsumoChild, TsumoDealer: points.TsumoDealer, Total: points.Total,
		},
	}, nil
}

// parseMeldsArg 解析并校验 score 工具的副露参数。
func parseMeldsArg(raw []meldArgs) ([]rules.Meld, error) {
	out := make([]rules.Meld, 0, len(raw))
	for i, meld := range raw {
		kind := strings.ToLower(strings.TrimSpace(meld.Kind))
		switch kind {
		case rules.MeldChi, rules.MeldPon, rules.MeldKanDaiminkan, rules.MeldKanShouminkan, rules.MeldKanAnkan:
			// 合法副露种类
		default:
			return nil, invalidParams("melds[%d].kind=%q 非法（chi/pon/kan_daiminkan/kan_shouminkan/kan_ankan）", i, meld.Kind)
		}
		tiles, err := parseHandArg(meld.Tiles, fmt.Sprintf("melds[%d].tiles", i))
		if err != nil {
			return nil, err
		}
		out = append(out, rules.Meld{Kind: kind, Tiles: tiles})
	}
	return out, nil
}

// ── mahjong.yaku_potential ───────────────────────────────────────────────

type yakuPotentialArgs struct {
	Hand []string `json:"hand"`
}

type potentialYaku struct {
	Name         string   `json:"name"`
	Han          int      `json:"han"`
	Yakuman      bool     `json:"yakuman,omitempty"`
	WinningTiles []string `json:"winning_tiles"`
}

type yakuPotentialResult struct {
	Hand         []string        `json:"hand"`
	Shanten      int             `json:"shanten"`
	WaitingTiles []string        `json:"waiting_tiles"`
	Yaku         []potentialYaku `json:"yaku"`
	Note         string          `json:"note,omitempty"`
}

func yakuPotentialTool() tool {
	return tool{
		Name: "mahjong.yaku_potential",
		Description: "列出 13 张听牌形手牌「立直即和」时可能成立的役种（按每个和了牌拆解 + Evaluate，" +
			"不含宝牌），以及可和了牌列表。",
		InputSchema: schemaObject(map[string]any{
			"hand": schemaStringArray("13 张门清听牌形手牌"),
		}, "hand"),
		Handler: handleYakuPotential,
	}
}

func handleYakuPotential(_ context.Context, args json.RawMessage) (any, error) {
	var in yakuPotentialArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, invalidParams("参数格式错误: %v", err)
	}
	hand, err := parseHandArg(in.Hand, "hand")
	if err != nil {
		return nil, err
	}
	if len(hand) != 13 {
		return nil, invalidParams("hand 必须是 13 张门清听牌形手牌，当前 %d 张（14 张和牌请用 mahjong.score）", len(hand))
	}
	base := rules.Shanten(hand, 0)
	if base == rules.ShantenInvalid {
		return nil, invalidParams("手牌无法计算向听（张数或牌面非法）")
	}

	visible := tileCounts(hand)
	waiting := make([]string, 0, tile.NumKinds)
	byName := make(map[string]*potentialYaku)
	ctx := rules.ScoreContext{Menzen: true, SeatWind: tile.East, RoundWind: tile.East}
	for kind := tile.Kind(0); kind < tile.NumKinds; kind++ {
		if remainingCopies(visible, kind) == 0 {
			continue
		}
		winning := tile.Tile{Kind: kind}
		tiles14 := make([]tile.Tile, 0, 14)
		tiles14 = append(tiles14, hand...)
		tiles14 = append(tiles14, winning)
		shape, ok := rules.Decompose(tiles14, nil, winning)
		if !ok {
			continue
		}
		waiting = append(waiting, kind.String())
		yaku, _, _, err := rules.Evaluate(shape, ctx)
		if err != nil {
			continue
		}
		for _, y := range yaku {
			entry, ok := byName[y.Name]
			if !ok {
				entry = &potentialYaku{Name: y.Name, Han: y.Han, Yakuman: y.Yakuman}
				byName[y.Name] = entry
			}
			if y.Han > entry.Han {
				entry.Han = y.Han
			}
			entry.WinningTiles = append(entry.WinningTiles, kind.String())
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if byName[names[i]].Han != byName[names[j]].Han {
			return byName[names[i]].Han > byName[names[j]].Han
		}
		return names[i] < names[j]
	})
	yakuOuts := make([]potentialYaku, 0, len(names))
	for _, name := range names {
		yakuOuts = append(yakuOuts, *byName[name])
	}

	out := yakuPotentialResult{
		Hand:         tilesToStrings(hand),
		Shanten:      base,
		WaitingTiles: waiting,
		Yaku:         yakuOuts,
	}
	if len(waiting) == 0 {
		out.Note = "当前不是听牌形（无和了牌），没有可立即成立的役种。"
	}
	return out, nil
}

// ── 参数 / schema 小工具 ─────────────────────────────────────────────────

// meldCountArg 校验 meldCount 与手牌张数是否满足 13-3*meldCount。
func meldCountArg(raw *int, handLen int) (int, error) {
	meldCount := 0
	if raw != nil {
		meldCount = *raw
	}
	if meldCount < 0 || meldCount > 4 {
		return 0, invalidParams("meldCount 必须在 0..4 之间")
	}
	if handLen+3*meldCount != 13 {
		return 0, invalidParams("hand 张数必须是 13-3*meldCount=%d，当前 %d 张", 13-3*meldCount, handLen)
	}
	return meldCount, nil
}

// parseWind 解析风位文本（E/S/W/N），空串默认东。
func parseWind(text string) (tile.Kind, error) {
	if text == "" {
		return tile.East, nil
	}
	kind, err := tile.ParseKind(text)
	if err != nil || !kind.IsWind() {
		return 0, invalidParams("风位 %q 非法（只能 E/S/W/N）", text)
	}
	return kind, nil
}

// schemaObject 构造 object schema；required 为空时不写 required 字段。
func schemaObject(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// schemaStringArray 构造 string[] schema。
func schemaStringArray(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": description,
	}
}

// schemaInteger 构造 integer schema；min/max 为 nil 时不写边界。
func schemaInteger(description string, min, max *int) map[string]any {
	s := map[string]any{"type": "integer", "description": description}
	if min != nil {
		s["minimum"] = *min
	}
	if max != nil {
		s["maximum"] = *max
	}
	return s
}

func intPtr(v int) *int { return &v }

// round1 把危险度保留 1 位小数，输出更可读。
func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
