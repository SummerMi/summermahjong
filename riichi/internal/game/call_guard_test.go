package game

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// ── 鸣牌「打牌前瞻」守卫（S9 seed 7 引擎 bug 的回归测试）────────────
//
// 场景：玩家已有 3 组副露、手牌 4 张（13 张总数）。若吃/碰后只剩 2 张且恰好全被
// 食替禁则（同种 + 筋）禁掉，则 Legal() 为空 → 驱动方 aborted。
// 修法：生成响应选项与执行鸣牌前都做前瞻，不满足「鸣牌后仍有牌可打」就不提供/拒绝。

// callGuardState 构造「seat3 刚摸 5m、seat0 已有 3 组副露 + 4 张手牌」的局面。
// seat2 手里有两张 5m（可碰），用来保证响应窗口不会因 seat0 无选项而立即结算。
func callGuardState(t *testing.T, seat0Hand string) *State {
	t.Helper()
	s := mustManualState(t, 42) // 手动响应窗口，才能观察到 Pending
	s.Rules.Kuikae = KuikaeSuji // 这组守卫测试针对「同种+筋」的严格口径
	s.Dealer = 0
	s.Scores = [4]int{25000, 25000, 25000, 25000}
	// seat0：3 组副露 + 4 张手牌（13 张）
	s.Melds[0] = []Meld{
		{Kind: MeldPon, Tiles: []tile.Tile{{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East}}, From: 1},
		{Kind: MeldPon, Tiles: []tile.Tile{{Kind: tile.South}, {Kind: tile.South}, {Kind: tile.South}}, From: 2},
		{Kind: MeldPon, Tiles: []tile.Tile{{Kind: tile.West}, {Kind: tile.West}, {Kind: tile.West}}, From: 3},
	}
	s.Hands[0] = mustHand(t, seat0Hand)
	if len(s.Hands[0]) != 4 {
		t.Fatalf("seat0 手牌应为 4 张，实际 %d", len(s.Hands[0]))
	}
	// seat1/2 各 13 张；seat2 有 5m5m（可碰）
	s.Hands[1] = mustHand(t, "1p1p1p3p3p3p7p8p9p2s2s2s9s")
	s.Hands[2] = mustHand(t, "5m5m1s1s1s3s3s3s7s8s9s2z2z")
	// seat3 刚摸 5m，准备打出
	s.Hands[3] = mustHand(t, "1m1m1m9m9m9m7p7p7p8p8p8p2z")
	drawn := mustTile(t, "5m")
	s.Hands[3] = append(s.Hands[3], drawn)
	s.Drawn = &drawn
	s.Turn = 3
	s.Phase = PhaseAwaitDiscard
	s.DrawCount = 1
	s.SeatDraws = [4]int{0, 0, 0, 1}
	s.Discards = [4][]tile.Tile{}
	return s
}

// TestChiOptionSkippedWhenKuikaeForbidsAll 吃 5m 后 kuikae=[5m,2m,8m] 把手牌全禁 → 不提供吃选项。
func TestChiOptionSkippedWhenKuikaeForbidsAll(t *testing.T) {
	// 手牌 2m4m6m8m：吃 5m 用 4m6m，剩 2m/8m，kuikae（5m 同种+筋）= {5m,2m,8m} → 全禁
	s := callGuardState(t, "2m4m6m8m")

	options := s.responseOptions(0, 3, mustTile(t, "5m"), ResponseToDiscard)
	for _, option := range options {
		if option.Kind == ActChi {
			t.Fatalf("吃后无牌可打，吃选项不应出现: %+v", option)
		}
	}

	// Apply 路径同样要拒绝（防止绕过选项直接提交）
	pending := &PendingResponses{Kind: ResponseToDiscard, From: 3, Tile: mustTile(t, "5m")}
	s.Pending = pending
	beforeHand := len(s.Hands[0])
	beforeMelds := len(s.Melds[0])
	chi := Action{Kind: ActChi, Seat: 0, Tile: mustTile(t, "5m"),
		Consumed: []tile.Tile{mustTile(t, "4m"), mustTile(t, "6m")}}
	if err := s.applyCallFromDiscard(pending, 0, chi); err == nil {
		t.Fatal("无牌可打的吃应被拒绝")
	}
	if len(s.Hands[0]) != beforeHand || len(s.Melds[0]) != beforeMelds {
		t.Fatalf("拒绝后不应改变状态: hand=%d melds=%d", len(s.Hands[0]), len(s.Melds[0]))
	}
	if len(s.Discards[3]) != 0 {
		t.Fatal("拒绝后牌河不应被移除牌")
	}
}

// TestChiOptionKeptWhenSomeTileRemains 只要吃后还有一张不被禁的牌，吃选项仍然存在。
func TestChiOptionKeptWhenSomeTileRemains(t *testing.T) {
	// 手牌 1m2m4m6m：吃 5m 用 4m6m，剩 1m/2m，kuikae={5m,2m,8m} → 1m 可打
	s := callGuardState(t, "1m2m4m6m")
	options := s.responseOptions(0, 3, mustTile(t, "5m"), ResponseToDiscard)
	foundChi := false
	for _, option := range options {
		if option.Kind == ActChi {
			foundChi = true
		}
	}
	if !foundChi {
		t.Fatalf("吃后仍有 1m 可打，吃选项应存在: %+v", options)
	}

	// 执行吃后 Legal() 必须非空（先模拟 seat3 的 5m 已在牌河）
	s.Discards[3] = []tile.Tile{mustTile(t, "5m")}
	s.Pending = &PendingResponses{Kind: ResponseToDiscard, From: 3, Tile: mustTile(t, "5m")}
	pending := s.Pending
	chi := Action{Kind: ActChi, Seat: 0, Tile: mustTile(t, "5m"),
		Consumed: []tile.Tile{mustTile(t, "4m"), mustTile(t, "6m")}}
	if err := s.applyCallFromDiscard(pending, 0, chi); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	if legal := s.Legal(); len(legal) == 0 {
		t.Fatal("吃后 Legal() 不应为空")
	}
}

// TestPonOptionSkippedWhenAllFourCopiesForbidden 碰后手牌只剩被禁的同种牌 → 不提供碰选项。
func TestPonOptionSkippedWhenAllFourCopiesForbidden(t *testing.T) {
	// 手牌 5m5m5m5m：碰 5m 用两张，剩 5m5m，kuikae 禁 5m → 无牌可打
	s := callGuardState(t, "5m5m5m5m")
	// 碰会留下 5m5m（被食替禁满）→ 碰选项必须被过滤；
	// 大明杠会先摸岭上再打（无食替限制），所以杠选项可以保留。
	options := s.responseOptions(0, 3, mustTile(t, "5m"), ResponseToDiscard)
	for _, option := range options {
		if option.Kind == ActPon {
			t.Fatalf("碰后无牌可打，碰选项不应出现: %+v", option)
		}
	}
}

// TestCallGuardKeepsWindowAlive 选项被过滤后窗口仍能正常推进（不会因 seat0 无选项而卡死）。
func TestCallGuardKeepsWindowAlive(t *testing.T) {
	s := callGuardState(t, "2m4m6m8m")
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 3, Tile: mustTile(t, "5m")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	if s.Phase != PhaseAwaitResponse {
		t.Fatalf("phase=%v want await_response（seat2 可碰）", s.Phase)
	}
	if s.Pending == nil {
		t.Fatal("应有响应窗口")
	}
	if len(s.Pending.Options[0]) != 0 {
		t.Fatalf("seat0 不应有响应选项: %+v", s.Pending.Options[0])
	}
	if !s.Pending.Decided[0] {
		t.Fatal("seat0 无选项应自动视为已 pass")
	}
	// seat2 的碰仍应可选，pass 后正常轮转
	if !s.Pending.hasOption(2, ActPon) {
		t.Fatalf("seat2 应有碰选项: %+v", s.Pending.Options[2])
	}
	if err := s.ResolveTimeout(); err != nil {
		t.Fatalf("响应超时结算失败: %v", err)
	}
	if s.Phase != PhaseAwaitDraw && s.Phase != PhaseAwaitDiscard {
		t.Fatalf("窗口结束后 phase=%v", s.Phase)
	}
	if s.Turn != 0 {
		t.Fatalf("全 pass 后应轮到 seat0 摸牌，实际 turn=%d", s.Turn)
	}
}
