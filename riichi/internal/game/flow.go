package game

import (
	"fmt"
	"sort"

	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// ── 响应窗口（契约 §2.2 / §7 的 ron > pon/kan > chi 优先级在这里统一实现）──

// PendingKind 表示响应窗口的触发来源。
type PendingKind uint8

const (
	// ResponseToDiscard 是打牌后的响应窗口（ron / pon / 大明杠 / 吃）。
	ResponseToDiscard PendingKind = iota
	// ResponseToShouminkan 是加杠后的抢杠窗口（只允许 ron）。
	ResponseToShouminkan
)

// String 返回响应窗口类型文本。
func (k PendingKind) String() string {
	switch k {
	case ResponseToDiscard:
		return "discard"
	case ResponseToShouminkan:
		return "shouminkan"
	default:
		return "unknown"
	}
}

// PendingResponses 是「一次响应窗口」的显式状态。
//
// 为什么单独建模而不是散在 if 里：响应是四家并发选择 + 优先级裁决，
// 拆成 Options（各座位可选项）/ Choices（已选择）/ Decided（是否已响应）
// 三个维度后，优先级逻辑只需在 resolveResponses 里实现一次，测试也容易构造。
type PendingResponses struct {
	Kind PendingKind
	From Seat      // 打牌者 / 加杠者
	Tile tile.Tile // 成为响应对象的牌（打出的牌 / 加杠的第四张）

	// Options 是各座位的可选动作（不含 pass；没有选项的座位自动视为已 pass）。
	Options [4][]Action
	// Choices 是各座位已选择的响应（nil 表示 pass 或尚未响应）。
	Choices [4]*Action
	// Decided 表示该座位是否已响应（含自动 pass）。
	Decided [4]bool
	// Passed 表示该座位放弃响应（用于振听判定）。
	Passed [4]bool
}

// pendingSeats 返回还未响应的座位。
func (p *PendingResponses) pendingSeats() []Seat {
	out := make([]Seat, 0, 4)
	for seat := Seat(0); seat < 4; seat++ {
		if !p.Decided[seat] {
			out = append(out, seat)
		}
	}
	return out
}

// allDecided 报告是否所有座位都已响应。
func (p *PendingResponses) allDecided() bool {
	for seat := Seat(0); seat < 4; seat++ {
		if !p.Decided[seat] {
			return false
		}
	}
	return true
}

// hasOption 报告某座位在本次窗口是否有指定类型的可选动作。
func (p *PendingResponses) hasOption(seat Seat, kind ActionKind) bool {
	for _, action := range p.Options[seat] {
		if action.Kind == kind {
			return true
		}
	}
	return false
}

// match 校验响应动作 a 是否属于该座位的合法选项。
func (p *PendingResponses) match(a Action) bool {
	if a.Seat >= 4 || a.Seat == p.From {
		return false
	}
	if a.Kind == ActPass {
		return true
	}
	for _, option := range p.Options[a.Seat] {
		if option.Kind != a.Kind {
			continue
		}
		if a.Kind == ActChi {
			if sameKindMultiset(option.Consumed, a.Consumed) {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// sameKindMultiset 比较两组牌是否包含相同的牌种与张数（吃/碰的 Consumed 校验）。
func sameKindMultiset(a, b []tile.Tile) bool {
	if len(a) != len(b) {
		return false
	}
	ka := make([]tile.Kind, len(a))
	kb := make([]tile.Kind, len(b))
	for i := range a {
		ka[i] = a[i].Kind
	}
	for i := range b {
		kb[i] = b[i].Kind
	}
	sort.Slice(ka, func(i, j int) bool { return ka[i] < ka[j] })
	sort.Slice(kb, func(i, j int) bool { return kb[i] < kb[j] })
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// 流局原因（契约 §2.2 ryuukyoku payload reason 的取值）。
const (
	// RyuukyokuNagashi 是流し満貫（规则开关，暂未实现）。
	RyuukyokuNagashi = "nagashi"
	// RyuukyokuKyuushu 是九种九牌。
	RyuukyokuKyuushu = "kyuushu"
	// RyuukyokuHaitei 是荒牌流局（牌山摸尽）。
	RyuukyokuHaitei = "haitei"
)

// ── 摸牌阶段 ──────────────────────────────────────────────────────

// applyDraw 处理摸牌：从活牌山摸一张，进入打牌阶段；摸空则荒牌流局。
func (s *State) applyDraw(a Action) error {
	if a.Kind != ActDraw {
		return fmt.Errorf("%w: 阶段 await_draw 只接受 draw，收到 %v", ErrIllegalAction, a.Kind)
	}
	if a.Seat != s.Turn {
		return fmt.Errorf("%w: seat %d 不是当前行动者 %d", ErrIllegalAction, a.Seat, s.Turn)
	}
	t, ok := s.Wall.Draw()
	if !ok {
		// 摸空：荒牌流局（结算写入事件，阶段进 RoundEnd）
		s.finishRyuukyoku(RyuukyokuHaitei, false)
		return fmt.Errorf("%w: 牌山已摸空（荒牌流局）", ErrIllegalAction)
	}
	s.Drawn = &t
	s.Hands[s.Turn] = append(s.Hands[s.Turn], t)
	s.Phase = PhaseAwaitDiscard
	s.DrawCount++
	s.SeatDraws[s.Turn]++
	// 轮到自己摸牌：同巡振听解除；一发窗口也结束
	s.TempFuriten[s.Turn] = false
	s.Ippatsu[s.Turn] = false
	s.lastDrawRinshan = false
	s.emitDraw(s.Turn, t)
	return nil
}

// ── 打牌阶段（摸牌后）────────────────────────────────────────────

// applyDiscardPhase 处理打牌阶段的自回合动作。
func (s *State) applyDiscardPhase(a Action) error {
	if a.Seat != s.Turn {
		return fmt.Errorf("%w: seat %d 不是当前行动者 %d", ErrIllegalAction, a.Seat, s.Turn)
	}
	switch a.Kind {
	case ActDiscard:
		return s.doDiscard(a.Seat, a.Tile, false)
	case ActRiichi:
		return s.doRiichi(a.Seat, a.Tile)
	case ActTsumo:
		return s.doTsumo(a.Seat)
	case ActKanAnkan:
		return s.doAnkan(a.Seat, a.Tile.Kind)
	case ActKanShouminkan:
		return s.doShouminkan(a.Seat, a.Tile.Kind)
	case ActKyuushu:
		return s.doKyuushu(a.Seat)
	default:
		return fmt.Errorf("%w: 阶段 await_discard 不接受动作 %v", ErrIllegalAction, a.Kind)
	}
}

// legalDiscardActions 返回打牌阶段当前座位的全部合法动作。
func (s *State) legalDiscardActions(seat Seat) []Action {
	if seat != s.Turn || s.Drawn == nil {
		return nil
	}
	acts := make([]Action, 0, len(s.Hands[seat])+8)

	// 立直后手牌固定：只能摸切；仍可自摸
	if s.Riichi[seat] {
		acts = append(acts, Action{Kind: ActDiscard, Seat: seat, Tile: *s.Drawn})
		if s.canTsumo(seat) {
			acts = append(acts, Action{Kind: ActTsumo, Seat: seat, Tile: *s.Drawn})
		}
		return acts
	}

	for _, tl := range s.Hands[seat] {
		if !s.kuikaeAllowed(seat, tl.Kind) {
			continue
		}
		acts = append(acts, Action{Kind: ActDiscard, Seat: seat, Tile: tl})
		if s.canRiichiWithDiscard(seat, tl) {
			acts = append(acts, Action{Kind: ActRiichi, Seat: seat, Tile: tl})
		}
	}
	if s.canTsumo(seat) {
		acts = append(acts, Action{Kind: ActTsumo, Seat: seat, Tile: *s.Drawn})
	}
	for _, kind := range s.ankanKinds(seat) {
		acts = append(acts, Action{Kind: ActKanAnkan, Seat: seat, Tile: tile.Tile{Kind: kind}})
	}
	for _, kind := range s.shouminkanKinds(seat) {
		acts = append(acts, Action{Kind: ActKanShouminkan, Seat: seat, Tile: tile.Tile{Kind: kind}})
	}
	if s.canKyuushu(seat) {
		acts = append(acts, Action{Kind: ActKyuushu, Seat: seat})
	}
	return acts
}

// doDiscard 执行打牌：移入手牌→牌河，然后打开响应窗口。
func (s *State) doDiscard(seat Seat, tl tile.Tile, declaredRiichi bool) error {
	if s.Phase != PhaseAwaitDiscard || seat != s.Turn || s.Drawn == nil {
		return fmt.Errorf("%w: 当前不能打牌", ErrIllegalAction)
	}
	if !s.kuikaeAllowed(seat, tl.Kind) {
		return fmt.Errorf("%w: 食替禁止：刚副露后不能打 %v", ErrIllegalAction, tl)
	}
	idx := s.findHandTile(seat, tl)
	if idx < 0 {
		return fmt.Errorf("%w: 手牌中没有 %v", ErrIllegalAction, tl)
	}
	if s.Riichi[seat] && !declaredRiichi {
		if tl != *s.Drawn {
			return fmt.Errorf("%w: 立直后只能摸切 %v", ErrIllegalAction, *s.Drawn)
		}
	}
	tsumogiri := *s.Drawn == tl

	hand := s.Hands[seat]
	s.Hands[seat] = append(hand[:idx:idx], hand[idx+1:]...)
	s.Discards[seat] = append(s.Discards[seat], tl)
	s.Drawn = nil
	s.Kuikae[seat] = nil
	s.lastDrawRinshan = false
	s.emitDiscard(seat, tl, tsumogiri, declaredRiichi)

	return s.openResponseWindow(seat, tl, ResponseToDiscard)
}

// doRiichi 执行立直：门清 + 听牌 + 点数 ≥1000，扣 1000 立直棒，宣言牌打出。
func (s *State) doRiichi(seat Seat, tl tile.Tile) error {
	if s.Phase != PhaseAwaitDiscard || seat != s.Turn || s.Drawn == nil {
		return fmt.Errorf("%w: 当前不能立直", ErrIllegalAction)
	}
	if s.Riichi[seat] {
		return fmt.Errorf("%w: 已经立直", ErrIllegalAction)
	}
	if len(s.Melds[seat]) != 0 {
		return fmt.Errorf("%w: 立直必须门清", ErrIllegalAction)
	}
	if s.Scores[seat] < 1000 {
		return fmt.Errorf("%w: 点数不足 1000，不能立直", ErrIllegalAction)
	}
	idx := s.findHandTile(seat, tl)
	if idx < 0 {
		return fmt.Errorf("%w: 手牌中没有 %v", ErrIllegalAction, tl)
	}
	// 宣言牌打出后仍必须听牌
	rest := make([]tile.Tile, 0, len(s.Hands[seat])-1)
	rest = append(rest, s.Hands[seat][:idx]...)
	rest = append(rest, s.Hands[seat][idx+1:]...)
	if len(s.waitingTiles(rest, s.Melds[seat])) == 0 {
		return fmt.Errorf("%w: 打 %v 后不听牌，不能立直", ErrIllegalAction, tl)
	}

	s.Riichi[seat] = true
	s.Ippatsu[seat] = true
	s.Scores[seat] -= 1000
	s.RiichiStix++
	s.emitPayload(EventRiichi, seat, &tl, map[string]any{
		"tile":             tl.String(),
		"ippatsu_eligible": true,
	})
	return s.doDiscard(seat, tl, true)
}

// doTsumo 处理自摸和了。
func (s *State) doTsumo(seat Seat) error {
	if s.Phase != PhaseAwaitDiscard || seat != s.Turn || s.Drawn == nil {
		return fmt.Errorf("%w: 当前不能自摸", ErrIllegalAction)
	}
	winning := *s.Drawn
	if _, ok := winShape(s.Hands[seat], s.Melds[seat], winning); !ok {
		return fmt.Errorf("%w: 手牌未和牌", ErrIllegalAction)
	}
	ctx := s.buildWinContext(seat, s.Hands[seat], winning, true, false)
	res, err := s.opts.ScoreProvider.ScoreWin(ctx)
	if err != nil {
		return fmt.Errorf("%w: 计分失败: %v", ErrIllegalAction, err)
	}
	if err := s.applyDeltas(res.Deltas); err != nil {
		return fmt.Errorf("%w: %v", ErrIllegalAction, err)
	}
	if s.RiichiStix > 0 {
		bonus := 1000 * s.RiichiStix
		s.Scores[seat] += bonus
		res.Deltas[seat] += bonus
		s.RiichiStix = 0
	}
	s.emitWinEvent(EventTsumo, seat, res, ctx)
	s.LastDealerRepeat = seat == s.Dealer
	s.Phase = PhaseRoundEnd
	s.emitRoundEnd(s.LastDealerRepeat)
	return nil
}

// doAnkan 处理暗杠：4 张同种→明刻改为杠子，翻宝牌，摸岭上牌。
func (s *State) doAnkan(seat Seat, kind tile.Kind) error {
	if s.Phase != PhaseAwaitDiscard || seat != s.Turn || s.Drawn == nil {
		return fmt.Errorf("%w: 当前不能暗杠", ErrIllegalAction)
	}
	if s.Riichi[seat] {
		return fmt.Errorf("%w: 立直后不可暗杠（简化规则）", ErrIllegalAction)
	}
	if s.countKind(seat, kind) < 4 {
		return fmt.Errorf("%w: 手牌中 %v 不足 4 张", ErrIllegalAction, kind)
	}
	hand, ok := removeKinds(s.Hands[seat], kind, kind, kind, kind)
	if !ok {
		return fmt.Errorf("%w: 暗杠移除手牌失败", ErrIllegalAction)
	}
	s.Hands[seat] = hand
	s.Drawn = nil
	meld := Meld{Kind: MeldKanAnkan, Tiles: fourTiles(kind), From: seat}
	s.Melds[seat] = append(s.Melds[seat], meld)
	s.emitCallEvent(seat, MeldKanAnkan, meld.Tiles, seat, nil, tile.Tile{Kind: kind})
	s.clearIppatsuAll()
	s.Calls++
	s.Turn = seat
	s.Phase = PhaseAwaitDiscard
	s.revealDora()
	return s.drawRinshanAndAwait(seat)
}

// doShouminkan 处理加杠：先开抢杠窗口（只允许 ron），无人抢杠再完成杠。
func (s *State) doShouminkan(seat Seat, kind tile.Kind) error {
	if s.Phase != PhaseAwaitDiscard || seat != s.Turn || s.Drawn == nil {
		return fmt.Errorf("%w: 当前不能加杠", ErrIllegalAction)
	}
	if s.Riichi[seat] {
		return fmt.Errorf("%w: 立直后不可加杠（简化规则）", ErrIllegalAction)
	}
	if !s.hasPon(seat, kind) {
		return fmt.Errorf("%w: 没有 %v 的碰，不能加杠", ErrIllegalAction, kind)
	}
	if s.countKind(seat, kind) < 1 {
		return fmt.Errorf("%w: 手牌中没有 %v，不能加杠", ErrIllegalAction, kind)
	}
	return s.openResponseWindow(seat, tile.Tile{Kind: kind}, ResponseToShouminkan)
}

// completeShouminkan 完成加杠（抢杠窗口无人荣和时调用）。
func (s *State) completeShouminkan(seat Seat, kind tile.Kind) error {
	idx := -1
	for i, meld := range s.Melds[seat] {
		if meld.Kind == MeldPon && len(meld.Tiles) > 0 && meld.Tiles[0].Kind == kind {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: 没有 %v 的碰，不能加杠", ErrIllegalAction, kind)
	}
	hand, ok := removeKinds(s.Hands[seat], kind)
	if !ok {
		return fmt.Errorf("%w: 手牌中没有 %v，不能加杠", ErrIllegalAction, kind)
	}
	s.Hands[seat] = hand
	s.Drawn = nil
	meld := s.Melds[seat][idx]
	meld.Kind = MeldKanShouminkan
	meld.Tiles = append(meld.Tiles, tile.Tile{Kind: kind})
	s.Melds[seat][idx] = meld
	s.emitCallEvent(seat, MeldKanShouminkan, meld.Tiles, meld.From, nil, tile.Tile{Kind: kind})
	s.clearIppatsuAll()
	s.Calls++
	s.Turn = seat
	s.Phase = PhaseAwaitDiscard
	s.revealDora()
	return s.drawRinshanAndAwait(seat)
}

// doKyuushu 处理九种九牌（配牌第一巡、无副露、幺九字牌 ≥9 种）。
func (s *State) doKyuushu(seat Seat) error {
	if !s.canKyuushu(seat) {
		return fmt.Errorf("%w: 不满足九种九牌条件", ErrIllegalAction)
	}
	s.finishRyuukyoku(RyuukyokuKyuushu, true)
	return nil
}

// ── 响应阶段 ─────────────────────────────────────────────────────

// openResponseWindow 建立响应窗口；无人可响应或自动模式时立即结算。
func (s *State) openResponseWindow(from Seat, tl tile.Tile, kind PendingKind) error {
	pending := &PendingResponses{
		Kind: kind,
		From: from,
		Tile: tl,
	}
	for seat := Seat(0); seat < 4; seat++ {
		if seat == from {
			pending.Decided[seat] = true
			continue
		}
		pending.Options[seat] = s.responseOptions(seat, from, tl, kind)
		if len(pending.Options[seat]) == 0 {
			pending.Decided[seat] = true
		}
	}
	s.Pending = pending

	if len(pending.pendingSeats()) == 0 {
		return s.resolveResponses()
	}
	s.Phase = PhaseAwaitResponse
	if !s.opts.ManualResponses {
		// 兼容模式（S6 现状）：四家自动 pass，立即结算，观感与 M1 一致
		return s.resolveResponses()
	}
	return nil
}

// responseOptions 计算某座位对 tl 的全部可选响应（不含 pass）。
func (s *State) responseOptions(seat, from Seat, tl tile.Tile, kind PendingKind) []Action {
	options := make([]Action, 0, 4)
	if s.canRon(seat, tl) {
		options = append(options, Action{Kind: ActRon, Seat: seat, Tile: tl})
	}
	if kind == ResponseToShouminkan {
		return options // 抢杠只允许荣和
	}
	// 立直后手牌固定，不能吃碰杠（暗杠在自回合处理）
	if s.Riichi[seat] {
		return options
	}
	// 大明杠与碰可以同时可选（手里 3 张时既可以杠也可以碰）
	if s.countKind(seat, tl.Kind) >= 3 {
		options = append(options, Action{
			Kind:     ActKanDaiminkan,
			Seat:     seat,
			Tile:     tl,
			Consumed: s.takeKinds(seat, tl.Kind, 3),
		})
	}
	if s.countKind(seat, tl.Kind) >= 2 {
		options = append(options, Action{
			Kind:     ActPon,
			Seat:     seat,
			Tile:     tl,
			Consumed: s.takeKinds(seat, tl.Kind, 2),
		})
	}
	// 吃：只能吃上家
	if seat == NextSeat(from) {
		for _, consumed := range s.chiConsumedOptions(seat, tl) {
			options = append(options, Action{
				Kind:     ActChi,
				Seat:     seat,
				Tile:     tl,
				Consumed: consumed,
			})
		}
	}
	return options
}

// applyResponse 处理响应窗口里的一个动作（ron / pon / kan / chi / pass）。
func (s *State) applyResponse(a Action) error {
	pending := s.Pending
	if pending == nil {
		return fmt.Errorf("%w: 当前没有响应窗口", ErrIllegalAction)
	}
	if a.Seat >= 4 || pending.Decided[a.Seat] {
		return fmt.Errorf("%w: seat %d 不在待响应集合", ErrIllegalAction, a.Seat)
	}
	if !pending.match(a) {
		return fmt.Errorf("%w: %v 不是 seat %d 的合法响应", ErrIllegalAction, a.Kind, a.Seat)
	}

	if a.Kind == ActPass {
		pending.Passed[a.Seat] = true
		// 放弃荣和：同巡振听；立直者放弃荣和则永久振听（立直后振听）
		if pending.hasOption(a.Seat, ActRon) {
			s.TempFuriten[a.Seat] = true
			if s.Riichi[a.Seat] {
				s.Furiten[a.Seat] = true
			}
		}
		pending.Decided[a.Seat] = true
	} else {
		choice := a
		pending.Choices[a.Seat] = &choice
		pending.Decided[a.Seat] = true
	}

	if pending.allDecided() {
		return s.resolveResponses()
	}
	return nil
}

// ResolveTimeout 模拟响应超时：所有未响应的座位视为 pass，然后结算。
func (s *State) ResolveTimeout() error {
	pending := s.Pending
	if pending == nil {
		return fmt.Errorf("%w: 当前没有响应窗口", ErrIllegalAction)
	}
	for seat := Seat(0); seat < 4; seat++ {
		if pending.Decided[seat] {
			continue
		}
		pending.Passed[seat] = true
		if pending.hasOption(seat, ActRon) {
			s.TempFuriten[seat] = true
			if s.Riichi[seat] {
				s.Furiten[seat] = true
			}
		}
		pending.Decided[seat] = true
	}
	return s.resolveResponses()
}

// resolveResponses 按优先级结算响应窗口：ron > pon/kan > chi > 全 pass。
func (s *State) resolveResponses() error {
	pending := s.Pending
	if pending == nil {
		return nil
	}
	s.Pending = nil

	// 1) 荣和优先（多家荣和按 Ruleset.DoubleRon）
	winners := s.ronWinners(pending)
	if len(winners) > 0 {
		if !s.Rules.DoubleRon && len(winners) > 1 {
			winners = winners[:1] // 头跳：最近的一家
		}
		return s.settleRons(pending, winners)
	}

	// 2) 加杠窗口：无人抢杠则完成加杠
	if pending.Kind == ResponseToShouminkan {
		return s.completeShouminkan(pending.From, pending.Tile.Kind)
	}

	// 3) 鸣牌：大明杠/碰 优先于 吃；多家时按从放铳者下家起的顺序取最近一家
	if caller, action, ok := s.pickCall(pending); ok {
		return s.applyCallFromDiscard(pending, caller, action)
	}

	// 4) 全 pass：下家摸牌
	s.Turn = NextSeat(pending.From)
	s.Phase = PhaseAwaitDraw
	return nil
}

// ronWinners 按「放铳者下家开始」的顺序收集选择荣和的座位。
func (s *State) ronWinners(pending *PendingResponses) []Seat {
	winners := make([]Seat, 0, 3)
	for i := 1; i <= 3; i++ {
		seat := Seat((int(pending.From) + i) % 4)
		if choice := pending.Choices[seat]; choice != nil && choice.Kind == ActRon {
			winners = append(winners, seat)
		}
	}
	return winners
}

// pickCall 按优先级挑选鸣牌者：kan/pon 优先（最近一家），其次 chi。
func (s *State) pickCall(pending *PendingResponses) (Seat, Action, bool) {
	// pon / kan：从下家起最近的一家
	for i := 1; i <= 3; i++ {
		seat := Seat((int(pending.From) + i) % 4)
		choice := pending.Choices[seat]
		if choice == nil {
			continue
		}
		if choice.Kind == ActKanDaiminkan || choice.Kind == ActPon {
			return seat, *choice, true
		}
	}
	// chi：只有下家可吃
	if choice := pending.Choices[NextSeat(pending.From)]; choice != nil && choice.Kind == ActChi {
		return NextSeat(pending.From), *choice, true
	}
	return 0, Action{}, false
}

// applyCallFromDiscard 执行吃/碰/大明杠：牌从牌河移除、手牌消耗、副露登记、轮转到鸣牌者。
func (s *State) applyCallFromDiscard(pending *PendingResponses, caller Seat, action Action) error {
	from := pending.From
	if len(s.Discards[from]) == 0 {
		return fmt.Errorf("%w: 牌河为空，无法鸣牌", ErrIllegalAction)
	}
	// 被鸣的牌必须是牌河最后一张
	riverTile := s.Discards[from][len(s.Discards[from])-1]
	if riverTile.Kind != pending.Tile.Kind {
		return fmt.Errorf("%w: 牌河最后一张不是被鸣的牌", ErrIllegalAction)
	}

	consumedKinds := make([]tile.Kind, 0, len(action.Consumed))
	for _, consumed := range action.Consumed {
		consumedKinds = append(consumedKinds, consumed.Kind)
	}
	hand, ok := removeKinds(s.Hands[caller], consumedKinds...)
	if !ok {
		return fmt.Errorf("%w: 手牌不足，无法鸣牌", ErrIllegalAction)
	}
	s.Hands[caller] = hand
	s.Discards[from] = s.Discards[from][:len(s.Discards[from])-1]

	meldKind := MeldChi
	switch action.Kind {
	case ActPon:
		meldKind = MeldPon
	case ActKanDaiminkan:
		meldKind = MeldKanDaiminkan
	}
	tiles := append(append([]tile.Tile{}, action.Consumed...), riverTile)
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].Kind < tiles[j].Kind })
	s.Melds[caller] = append(s.Melds[caller], Meld{Kind: meldKind, Tiles: tiles, From: from})
	s.emitCallEvent(caller, meldKind, tiles, from, action.Consumed, pending.Tile)

	s.clearIppatsuAll()
	s.Calls++
	s.Turn = caller
	s.Phase = PhaseAwaitDiscard
	s.Drawn = nil
	s.Kuikae[caller] = kuikaeForbiddenKinds(meldKind, pending.Tile.Kind)

	if meldKind == MeldKanDaiminkan {
		s.revealDora()
		return s.drawRinshanAndAwait(caller)
	}
	return nil
}

// settleRons 结算一家或多家荣和。
func (s *State) settleRons(pending *PendingResponses, winners []Seat) error {
	type winOutcome struct {
		seat    Seat
		result  WinResult
		context WinContext
	}
	outcomes := make([]winOutcome, 0, len(winners))
	for _, winner := range winners {
		hand := make([]tile.Tile, 0, len(s.Hands[winner])+1)
		hand = append(hand, s.Hands[winner]...)
		hand = append(hand, pending.Tile)
		ctx := s.buildWinContext(winner, hand, pending.Tile, false, pending.Kind == ResponseToShouminkan)
		ctx.RonTarget = pending.From
		result, err := s.opts.ScoreProvider.ScoreWin(ctx)
		if err != nil {
			return fmt.Errorf("%w: 计分失败: %v", ErrIllegalAction, err)
		}
		if err := s.applyDeltas(result.Deltas); err != nil {
			return fmt.Errorf("%w: %v", ErrIllegalAction, err)
		}
		outcomes = append(outcomes, winOutcome{seat: winner, result: result, context: ctx})
	}

	// 立直棒：全部归最近的和了者（头跳顺序的第一个）
	first := outcomes[0]
	if s.RiichiStix > 0 {
		bonus := 1000 * s.RiichiStix
		s.Scores[first.seat] += bonus
		first.result.Deltas[first.seat] += bonus
		s.RiichiStix = 0
		outcomes[0] = first
	}

	dealerRepeat := false
	for _, outcome := range outcomes {
		if outcome.seat == s.Dealer {
			dealerRepeat = true
		}
	}
	for _, outcome := range outcomes {
		s.emitWinEvent(EventRon, outcome.seat, outcome.result, outcome.context)
	}
	s.LastDealerRepeat = dealerRepeat
	s.Phase = PhaseRoundEnd
	s.emitRoundEnd(dealerRepeat)
	return nil
}

// ── 和了/流局结算 ────────────────────────────────────────────────

// buildWinContext 组装计分上下文。
func (s *State) buildWinContext(seat Seat, hand []tile.Tile, winning tile.Tile, tsumo, chankan bool) WinContext {
	return WinContext{
		Seat:           seat,
		DealerSeat:     s.Dealer,
		Tsumo:          tsumo,
		Dealer:         seat == s.Dealer,
		RoundWind:      s.RoundWind,
		SeatWind:       s.CurrentWind(seat),
		Hand:           hand,
		Melds:          s.Melds[seat],
		WinningTile:    winning,
		Riichi:         s.Riichi[seat],
		Ippatsu:        s.Riichi[seat] && s.Ippatsu[seat],
		Rinshan:        tsumo && s.lastDrawRinshan,
		Chankan:        chankan,
		Haitei:         tsumo && s.Wall.Remaining() == 0,
		Houtei:         !tsumo && s.Wall.Remaining() == 0,
		DoraIndicators: s.Wall.DoraIndicators(),
		UraIndicators:  s.Wall.UraIndicators(),
		Honba:          s.Honba,
		RiichiSticks:   s.RiichiStix,
	}
}

// applyDeltas 应用四家点数变化；要求总和为 0（防止计分实现算错）。
func (s *State) applyDeltas(deltas [4]int) error {
	sum := 0
	for _, delta := range deltas {
		sum += delta
	}
	if sum != 0 {
		return fmt.Errorf("game: 点数变化总和应为 0，实际 %d", sum)
	}
	for seat := 0; seat < 4; seat++ {
		s.Scores[seat] += deltas[seat]
	}
	return nil
}

// finishRyuukyoku 结算流局：听牌料分配 + 流局事件 + round_end。
//
// abortive=true 表示途中流局（九种九牌等）：不做听牌料，亲家连庄。
func (s *State) finishRyuukyoku(reason string, abortive bool) {
	var tenpai [4]bool
	var deltas [4]int
	tenpaiSeats := make([]int, 0, 4)

	if !abortive {
		tenpaiCount := 0
		for seat := Seat(0); seat < 4; seat++ {
			tenpai[seat] = s.isTenpai(seat)
			if tenpai[seat] {
				tenpaiCount++
				tenpaiSeats = append(tenpaiSeats, int(seat))
			}
		}
		if tenpaiCount > 0 && tenpaiCount < 4 {
			notenCount := 4 - tenpaiCount
			notenPay := 3000 / notenCount
			tenpaiGain := 3000 / tenpaiCount
			for seat := Seat(0); seat < 4; seat++ {
				if tenpai[seat] {
					deltas[seat] = tenpaiGain
				} else {
					deltas[seat] = -notenPay
				}
			}
			if err := s.applyDeltas(deltas); err != nil {
				// 听牌料计算只涉及 3000 分配，理论不会失败；失败则记 error 事件并保持点数不变
				s.emitPayload(EventError, s.Dealer, nil, map[string]any{
					"code":    "RYUUKYOKU_SETTLE",
					"message": err.Error(),
				})
			}
		}
	}

	dealerRepeat := true
	if !abortive {
		dealerRepeat = tenpai[s.Dealer]
	}
	s.emitPayload(EventRyuukyoku, s.Dealer, nil, map[string]any{
		"reason":       reason,
		"tenpai_seats": tenpaiSeats,
		"score_deltas": deltas,
	})
	s.LastDealerRepeat = dealerRepeat
	s.Phase = PhaseRoundEnd
	s.emitRoundEnd(dealerRepeat)
}

// emitRoundEnd 追加本局结束事件（honba 为下一局本场数）。
func (s *State) emitRoundEnd(dealerRepeat bool) {
	nextHonba := 0
	if dealerRepeat {
		nextHonba = s.Honba + 1
	}
	s.emitPayload(EventRoundEnd, s.Dealer, nil, map[string]any{
		"dealer_repeat": dealerRepeat,
		"honba":         nextHonba,
		"riichi_sticks": s.RiichiStix,
	})
}

// StartNextRound 进入下一局；南 4 结束或有人点数 <0（トビ終了）时进终局。
func (s *State) StartNextRound() error {
	if s.Phase != PhaseRoundEnd {
		return fmt.Errorf("%w: 当前阶段 %v 不能开下一局", errRoundNotEnded, s.Phase)
	}
	if s.gonnaEnd() {
		s.Phase = PhaseGameEnd
		s.emitPayload(EventGameEnd, s.Dealer, nil, map[string]any{
			"final_scores": s.Scores,
		})
		return nil
	}

	if s.LastDealerRepeat {
		s.Honba++
	} else {
		s.Honba = 0
		s.Dealer = NextSeat(s.Dealer)
		s.Kyoku++
		if s.Kyoku > maxKyokuInWind {
			s.Kyoku = 1
			s.RoundWind++
		}
	}

	// 新牌山：由 seed + 局信息派生，保证回放可复现
	seed := s.Seed + int64(s.RoundWind)*1000003 + int64(s.Kyoku)*7919 + int64(s.Honba)*104729
	wall, err := tile.NewWall(seed, s.Rules.RedFives)
	if err != nil {
		return err
	}
	hands, err := wall.Deal()
	if err != nil {
		return err
	}

	s.Wall = wall
	s.Hands = hands
	s.Melds = [4][]Meld{}
	s.Discards = [4][]tile.Tile{}
	s.Drawn = nil
	s.Riichi = [4]bool{}
	s.Furiten = [4]bool{}
	s.TempFuriten = [4]bool{}
	s.Ippatsu = [4]bool{}
	s.Kuikae = [4][]tile.Kind{}
	s.Pending = nil
	s.DrawCount = 0
	s.SeatDraws = [4]int{}
	s.Calls = 0
	s.lastDrawRinshan = false
	s.Turn = s.Dealer
	s.Phase = PhaseAwaitDraw
	s.emitRoundStart()
	return nil
}

// gonnaEnd 判断整场是否结束：南 4 之后，或有人 トビ（点数 < 0）。
func (s *State) gonnaEnd() bool {
	roundsPlayed := int(s.RoundWind)*maxKyokuInWind + (s.Kyoku - 1)
	roundLimit := s.Rules.RoundLimit
	if roundLimit <= 0 {
		roundLimit = DefaultRuleset().RoundLimit
	}
	if roundsPlayed+1 >= roundLimit {
		return true
	}
	for seat := 0; seat < 4; seat++ {
		if s.Scores[seat] < 0 {
			return true
		}
	}
	return false
}

// ── 牌型/听牌/振听辅助 ───────────────────────────────────────────

// winShape 用 rules 判和牌，并把 gate.Meld 适配成 rules 期望的「14 张完整牌形」。
//
// rules S3/S4 目前的 Decompose 入参约定是 14 张含和了牌的门清手牌（副露尚未支持），
// 所以这里把副露按面子摊回手牌（杠只取 3 张参与牌型）再调用；等 rules 支持 melds 入参
// （契约 v1.2 的 S5 适配项）后，把本函数替换成 Decompose(hand, rulesMelds, win) 即可。
func winShape(hand []tile.Tile, melds []Meld, winning tile.Tile) (rules.WinShape, bool) {
	if len(melds) == 0 {
		return rules.Decompose(hand, nil, winning)
	}
	if len(hand)+3*len(melds) != 14 {
		return rules.WinShape{}, false
	}
	flat := make([]tile.Tile, 0, 14)
	flat = append(flat, hand...)
	for _, meld := range melds {
		tiles := meld.Tiles
		if len(tiles) > 3 {
			tiles = tiles[:3] // 杠按 1 组面子参与牌型
		}
		flat = append(flat, tiles...)
	}
	if len(flat) != 14 {
		return rules.WinShape{}, false
	}
	shape, ok := rules.Decompose(flat, nil, winning)
	if !ok || shape.Pattern != rules.PatternStandard {
		// 有副露时七对/国士不成立
		return rules.WinShape{}, false
	}
	return shape, true
}

// waitingTiles 计算一手「13-3*副露数」张手牌的听牌列表。
func (s *State) waitingTiles(hand []tile.Tile, melds []Meld) []tile.Tile {
	if len(hand)+3*len(melds) != 13 {
		return nil
	}
	waits := make([]tile.Tile, 0, 13)
	counts := make(map[tile.Kind]int, 34)
	for _, tl := range hand {
		counts[tl.Kind]++
	}
	for kind := tile.Kind(0); kind < tile.NumKinds; kind++ {
		if counts[kind] >= 4 {
			continue
		}
		candidate := tile.Tile{Kind: kind}
		extended := make([]tile.Tile, 0, len(hand)+1)
		extended = append(extended, hand...)
		extended = append(extended, candidate)
		if _, ok := winShape(extended, melds, candidate); ok {
			waits = append(waits, candidate)
		}
	}
	return waits
}

// isTenpai 报告某座位当前是否听牌（手牌应为 13-3m 张）。
func (s *State) isTenpai(seat Seat) bool {
	need := 13 - 3*s.meldCount(seat)
	if len(s.Hands[seat]) != need {
		return false
	}
	return len(s.waitingTiles(s.Hands[seat], s.Melds[seat])) > 0
}

// canRon 报告某座位是否能荣和 tl（含振听判定）。
func (s *State) canRon(seat Seat, tl tile.Tile) bool {
	if s.Furiten[seat] || s.TempFuriten[seat] {
		return false
	}
	need := 13 - 3*s.meldCount(seat)
	if len(s.Hands[seat]) != need {
		return false
	}
	hand := make([]tile.Tile, 0, len(s.Hands[seat])+1)
	hand = append(hand, s.Hands[seat]...)
	hand = append(hand, tl)
	if _, ok := winShape(hand, s.Melds[seat], tl); !ok {
		return false
	}
	// 先判和牌再算听牌列表（听牌列表要对 34 种牌各判一次胡，成本更高）
	waits := s.waitingTiles(s.Hands[seat], s.Melds[seat])
	inWaits := false
	for _, wait := range waits {
		if wait.Kind == tl.Kind {
			inWaits = true
			break
		}
	}
	if !inWaits {
		return false
	}
	// 捨て牌振听：自己牌河里出现过自己听的任何牌种 → 不能荣和
	for _, wait := range waits {
		for _, discard := range s.Discards[seat] {
			if discard.Kind == wait.Kind {
				return false
			}
		}
	}
	return true
}

// canTsumo 报告当前打牌阶段是否自摸和牌。
func (s *State) canTsumo(seat Seat) bool {
	if s.Drawn == nil {
		return false
	}
	_, ok := winShape(s.Hands[seat], s.Melds[seat], *s.Drawn)
	return ok
}

// canRiichiWithDiscard 报告「打 tl 后立直」是否合法。
func (s *State) canRiichiWithDiscard(seat Seat, tl tile.Tile) bool {
	if s.Riichi[seat] || len(s.Melds[seat]) != 0 || s.Scores[seat] < 1000 {
		return false
	}
	idx := s.findHandTile(seat, tl)
	if idx < 0 {
		return false
	}
	rest := make([]tile.Tile, 0, len(s.Hands[seat])-1)
	rest = append(rest, s.Hands[seat][:idx]...)
	rest = append(rest, s.Hands[seat][idx+1:]...)
	return len(s.waitingTiles(rest, s.Melds[seat])) > 0
}

// canKyuushu 报告是否满足九种九牌条件。
func (s *State) canKyuushu(seat Seat) bool {
	if s.Phase != PhaseAwaitDiscard || seat != s.Turn || s.Drawn == nil {
		return false
	}
	if s.Calls != 0 || s.SeatDraws[seat] != 1 {
		return false
	}
	for i := 0; i < 4; i++ {
		if len(s.Melds[i]) != 0 {
			return false
		}
	}
	distinct := make(map[tile.Kind]bool, 13)
	for _, tl := range s.Hands[seat] {
		if tl.Kind.IsTerminalOrHonor() {
			distinct[tl.Kind] = true
		}
	}
	return len(distinct) >= 9
}

// ankanKinds 返回当前可暗杠的牌种。
func (s *State) ankanKinds(seat Seat) []tile.Kind {
	if s.Riichi[seat] {
		return nil
	}
	counts := make(map[tile.Kind]int, 14)
	for _, tl := range s.Hands[seat] {
		counts[tl.Kind]++
	}
	kinds := make([]tile.Kind, 0, 2)
	for kind := tile.Kind(0); kind < tile.NumKinds; kind++ {
		if counts[kind] >= 4 {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// shouminkanKinds 返回当前可加杠的牌种（已有碰且手里有第 4 张）。
func (s *State) shouminkanKinds(seat Seat) []tile.Kind {
	if s.Riichi[seat] {
		return nil
	}
	kinds := make([]tile.Kind, 0, 2)
	for _, meld := range s.Melds[seat] {
		if meld.Kind != MeldPon || len(meld.Tiles) == 0 {
			continue
		}
		kind := meld.Tiles[0].Kind
		if s.countKind(seat, kind) >= 1 {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// hasPon 报告某座位是否有 kind 的碰。
func (s *State) hasPon(seat Seat, kind tile.Kind) bool {
	for _, meld := range s.Melds[seat] {
		if meld.Kind == MeldPon && len(meld.Tiles) > 0 && meld.Tiles[0].Kind == kind {
			return true
		}
	}
	return false
}

// chiConsumedOptions 返回吃 tl 时可行的「手牌两张」组合。
func (s *State) chiConsumedOptions(seat Seat, tl tile.Tile) [][]tile.Tile {
	if tl.Kind.IsHonor() {
		return nil
	}
	counts := make(map[tile.Kind]int, 14)
	for _, handTile := range s.Hands[seat] {
		counts[handTile.Kind]++
	}
	number := tl.Kind.Number()
	base := (int(tl.Kind) / 9) * 9
	options := make([][]tile.Tile, 0, 3)
	try := func(a, b int) {
		if a < 1 || a > 9 || b < 1 || b > 9 {
			return
		}
		kindA := tile.Kind(base + a - 1)
		kindB := tile.Kind(base + b - 1)
		if counts[kindA] < 1 || counts[kindB] < 1 {
			return
		}
		consumed := append(s.takeKinds(seat, kindA, 1), s.takeKinds(seat, kindB, 1)...)
		options = append(options, consumed)
	}
	try(number-2, number-1) // tl 作顺子末张
	try(number-1, number+1) // tl 作中间张
	try(number, number+1)   // tl 作顺子首张
	return options
}

// takeKinds 从手牌中取 n 张指定牌种的牌（不修改手牌，仅用于构造选项）。
func (s *State) takeKinds(seat Seat, kind tile.Kind, n int) []tile.Tile {
	out := make([]tile.Tile, 0, n)
	for _, tl := range s.Hands[seat] {
		if tl.Kind == kind && len(out) < n {
			out = append(out, tl)
		}
	}
	return out
}

// countKind 返回手牌中某牌种的张数。
func (s *State) countKind(seat Seat, kind tile.Kind) int {
	count := 0
	for _, tl := range s.Hands[seat] {
		if tl.Kind == kind {
			count++
		}
	}
	return count
}

// findHandTile 返回与 tl 完全相同的牌在下标（找不到返回 -1）。
func (s *State) findHandTile(seat Seat, tl tile.Tile) int {
	for i, handTile := range s.Hands[seat] {
		if handTile == tl {
			return i
		}
	}
	return -1
}

// removeKinds 依次移除指定牌种的牌，返回新切片与是否成功。
func removeKinds(hand []tile.Tile, kinds ...tile.Kind) ([]tile.Tile, bool) {
	out := append([]tile.Tile(nil), hand...)
	for _, kind := range kinds {
		idx := -1
		for i, handTile := range out {
			if handTile.Kind == kind {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, false
		}
		out = append(out[:idx:idx], out[idx+1:]...)
	}
	return out, true
}

// fourTiles 构造 4 张同种牌（暗杠用）。
func fourTiles(kind tile.Kind) []tile.Tile {
	out := make([]tile.Tile, 4)
	for i := range out {
		out[i] = tile.Tile{Kind: kind}
	}
	return out
}

// kuikaeForbiddenKinds 计算吃/碰后立即打牌的禁则牌种（食替禁止，天鳳式）。
//
//   - 碰：不能打刚碰的牌（同种）。
//   - 吃：不能打刚吃的牌，以及它的「筋」（同花色 ±3）——
//     例如吃 1m 后不能打 1m 与 4m。
func kuikaeForbiddenKinds(kind MeldKind, called tile.Kind) []tile.Kind {
	if kind == MeldPon {
		return []tile.Kind{called}
	}
	if kind != MeldChi {
		return nil
	}
	forbidden := []tile.Kind{called}
	if called.IsHonor() {
		return forbidden
	}
	number := called.Number()
	base := (int(called) / 9) * 9
	for _, delta := range []int{-3, 3} {
		suji := number + delta
		if suji >= 1 && suji <= 9 {
			forbidden = append(forbidden, tile.Kind(base+suji-1))
		}
	}
	return forbidden
}

// kuikaeAllowed 报告某座位现在是否可以打 kind。
func (s *State) kuikaeAllowed(seat Seat, kind tile.Kind) bool {
	for _, forbidden := range s.Kuikae[seat] {
		if forbidden == kind {
			return false
		}
	}
	return true
}

// clearIppatsuAll 清空所有一发标记（任何副露/杠都会打断一发）。
func (s *State) clearIppatsuAll() {
	for seat := 0; seat < 4; seat++ {
		s.Ippatsu[seat] = false
	}
}

// drawRinshanAndAwait 摸岭上牌并进入打牌阶段（杠后调用）。
func (s *State) drawRinshanAndAwait(seat Seat) error {
	t, ok := s.Wall.DrawRinshan()
	if !ok {
		return fmt.Errorf("%w: 岭上牌已用完", ErrIllegalAction)
	}
	s.Drawn = &t
	s.Hands[seat] = append(s.Hands[seat], t)
	s.SeatDraws[seat]++
	s.TempFuriten[seat] = false
	s.lastDrawRinshan = true
	s.Turn = seat
	s.Phase = PhaseAwaitDiscard
	s.emitDraw(seat, t)
	return nil
}

// ── 事件发射 ────────────────────────────────────────────────────

// emitRoundStart 追加局开始事件。
func (s *State) emitRoundStart() {
	s.emitPayload(EventRoundStart, s.Dealer, nil, map[string]any{
		"round_wind": int(s.RoundWind),
		"kyoku":      s.Kyoku,
		"honba":      s.Honba,
		"dealer":     int(s.Dealer),
		"scores":     s.Scores,
	})
}

// emitDraw 追加摸牌事件（payload 与 S6 的 enrichEvent 保持同名同值）。
func (s *State) emitDraw(seat Seat, tl tile.Tile) {
	s.emitPayload(EventDraw, seat, &tl, map[string]any{
		"tile":           tl.String(),
		"tile_red":       tl.Aka,
		"wall_remaining": s.Wall.Remaining(),
	})
}

// emitDiscard 追加打牌事件。
func (s *State) emitDiscard(seat Seat, tl tile.Tile, tsumogiri, riichiDeclared bool) {
	s.emitPayload(EventDiscard, seat, &tl, map[string]any{
		"tile":            tl.String(),
		"tile_red":        tl.Aka,
		"tsumogiri":       tsumogiri,
		"riichi_declared": riichiDeclared,
	})
}

// emitCallEvent 追加吃/碰/杠事件（统一用 EventChi/EventPon/EventKan，payload 带 kan_type）。
func (s *State) emitCallEvent(seat Seat, kind MeldKind, tiles []tile.Tile, from Seat, consumed []tile.Tile, called tile.Tile) {
	eventKind := EventChi
	switch kind {
	case MeldPon:
		eventKind = EventPon
	case MeldKanDaiminkan, MeldKanShouminkan, MeldKanAnkan:
		eventKind = EventKan
	}
	payload := map[string]any{
		"tile":      called.String(),
		"tiles":     tileStrings(tiles),
		"consumed":  tileStrings(consumed),
		"from":      int(from),
		"kan_type":  string(kind),
		"meld_kind": string(kind),
	}
	s.emitPayload(eventKind, seat, &called, payload)
}

// emitWinEvent 追加和了事件。
func (s *State) emitWinEvent(kind EventKind, seat Seat, result WinResult, ctx WinContext) {
	payload := map[string]any{
		"winning_tile": ctx.WinningTile.String(),
		"yaku":         result.Yaku,
		"han":          result.Han,
		"fu":           result.Fu,
		"points":       result.Deltas[seat],
		"score_deltas": result.Deltas,
		"tsumo":        ctx.Tsumo,
		"riichi":       ctx.Riichi,
		"ippatsu":      ctx.Ippatsu,
		"rinshan":      ctx.Rinshan,
		"chankan":      ctx.Chankan,
		"haitei":       ctx.Haitei,
		"houtei":       ctx.Houtei,
	}
	s.emitPayload(kind, seat, &ctx.WinningTile, payload)
}

// revealDora 追加宝牌翻开事件（杠后调用）。
func (s *State) revealDora() {
	indicators := s.Wall.DoraIndicators()
	if len(indicators) == 0 {
		return
	}
	indicator := indicators[len(indicators)-1]
	s.emitPayload(EventDoraReveal, s.Turn, &indicator, map[string]any{
		"indicator": indicator.String(),
		"dora":      tileStrings(doraKinds(indicators)),
		"is_ura":    false,
	})
}
