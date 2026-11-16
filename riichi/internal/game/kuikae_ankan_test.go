package game

import (
	"testing"
)

// ── 食替口径可配（2026-11-16）──────────────────────────────────

// chiThenTryDiscard 让 seat1 吃 seat0 打的 5p，再尝试打 text；返回打牌错误。
func chiThenTryDiscard(t *testing.T, rules KuikaeRule, extraHand string, discardText string) error {
	t.Helper()
	s := scenario(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p", // seat0 打 5p
		"3p4p123m456m789m11p",          // seat1 可吃
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p6p8p9p1z2z3z",
	)
	s.Rules.Kuikae = rules
	for _, text := range splitHandText(t, extraHand) {
		s.Hands[1] = append(s.Hands[1], mustTile(t, text))
	}
	applyDiscard(t, s, 0, "5p")
	chi := Action{Kind: ActChi, Seat: 1, Tile: mustTile(t, "5p"),
		Consumed: s.Pending.Options[1][len(s.Pending.Options[1])-1].Consumed}
	if err := s.Apply(chi); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	return s.Apply(Action{Kind: ActDiscard, Seat: 1, Tile: mustTile(t, discardText)})
}

// splitHandText 把 "2p 8p" 拆成牌名列表。
func splitHandText(t *testing.T, text string) []string {
	t.Helper()
	if text == "" {
		return nil
	}
	out := make([]string, 0, 4)
	cur := ""
	for _, r := range text {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// TestKuikaeSameDefaultAllowsSuji 默认口径（天凤）：吃 5p 后可以打 2p/8p（筋）。
func TestKuikaeSameDefaultAllowsSuji(t *testing.T) {
	if err := chiThenTryDiscard(t, KuikaeSame, "2p 8p", "2p"); err != nil {
		t.Fatalf("天凤口径下吃后打筋牌 2p 应被允许: %v", err)
	}
}

// TestKuikaeSameDefaultForbidsSameTile 默认口径仍然禁止打刚吃的同种牌。
func TestKuikaeSameDefaultForbidsSameTile(t *testing.T) {
	if err := chiThenTryDiscard(t, KuikaeSame, "5p", "5p"); err == nil {
		t.Fatal("吃后打同种 5p 应被食替禁止拦截")
	}
}

// TestKuikaeSujiForbidsSuji 严格口径：吃 5p 后打 2p/8p 被禁。
func TestKuikaeSujiForbidsSuji(t *testing.T) {
	if err := chiThenTryDiscard(t, KuikaeSuji, "2p 8p", "2p"); err == nil {
		t.Fatal("严格口径下吃后打筋牌 2p 应被拦截")
	}
}

// TestKuikaeNoneAllowsSameTile 关闭食替：吃后可以打同种牌。
func TestKuikaeNoneAllowsSameTile(t *testing.T) {
	if err := chiThenTryDiscard(t, KuikaeNone, "5p", "5p"); err != nil {
		t.Fatalf("KuikaeNone 下吃后打同种 5p 应被允许: %v", err)
	}
}

// ── 立直后暗杠（2026-11-16）────────────────────────────────────

// riichiAnkanState 构造「seat0 已立直、刚摸到第 4 张 kind」的局面。
func riichiAnkanState(t *testing.T, handText string) *State {
	t.Helper()
	s := mustState(t, 42)
	s.Hands[0] = mustHand(t, handText)
	drawn := s.Hands[0][len(s.Hands[0])-1]
	s.Drawn = &drawn
	s.Riichi[0] = true
	s.Phase = PhaseAwaitDiscard
	s.Turn = 0
	s.DrawCount = 2
	s.SeatDraws = [4]int{2, 0, 0, 0}
	return s
}

// TestRiichiAnkanAllowedWhenWaitsUnchanged 立直后暗杠不改变听牌 → 允许。
func TestRiichiAnkanAllowedWhenWaitsUnchanged(t *testing.T) {
	// 立直形：111m 234m 567m 789p 9p（单骑 9p）；刚摸到第 4 张 1m。
	s := riichiAnkanState(t, "234m 567m 789p 9p 1111m")
	if err := s.Apply(Action{Kind: ActKanAnkan, Seat: 0, Tile: mustTile(t, "1m")}); err != nil {
		t.Fatalf("不改变听牌的暗杠应被允许: %v", err)
	}
	if len(s.Melds[0]) != 1 || s.Melds[0][0].Kind != MeldKanAnkan {
		t.Fatalf("应登记一组暗杠，实际 %+v", s.Melds[0])
	}
	if s.Phase != PhaseAwaitDiscard || s.Drawn == nil {
		t.Fatalf("暗杠后应摸岭上牌进打牌阶段，phase=%v drawn=%v", s.Phase, s.Drawn)
	}
	// 立直后仍只能摸切（岭上牌）
	legal := s.Legal()
	if len(legal) == 0 || legal[0].Kind != ActDiscard {
		t.Fatalf("暗杠后 Legal() 应至少包含摸切，实际 %+v", legal)
	}
}

// TestRiichiAnkanRejectedWhenWaitsChange 立直后暗杠会改变听牌 → 拒绝。
func TestRiichiAnkanRejectedWhenWaitsChange(t *testing.T) {
	// 立直形：111m 23m 456p 789p 99s 听 1m/4m；摸到第 4 张 1m 后杠掉全部 1m，
	// 1m 的听牌消失（只剩 4m）→ 应拒绝。
	s := riichiAnkanState(t, "111m 23m 456p 789p 99s 1m")
	beforeHand := len(s.Hands[0])
	beforeMelds := len(s.Melds[0])
	if err := s.Apply(Action{Kind: ActKanAnkan, Seat: 0, Tile: mustTile(t, "1m")}); err == nil {
		t.Fatal("改变听牌的立直后暗杠应被拒绝")
	}
	if len(s.Hands[0]) != beforeHand || len(s.Melds[0]) != beforeMelds {
		t.Fatalf("拒绝后状态不应改变: hand=%d melds=%d", len(s.Hands[0]), len(s.Melds[0]))
	}
}

// TestRiichiAnkanNotOfferedWhenWaitsChange Legal() 不提供会改变听牌的暗杠。
func TestRiichiAnkanNotOfferedWhenWaitsChange(t *testing.T) {
	s := riichiAnkanState(t, "111m 23m 456p 789p 99s 1m")
	for _, action := range s.Legal() {
		if action.Kind == ActKanAnkan {
			t.Fatalf("改变听牌的暗杠不应出现在 Legal(): %+v", action)
		}
	}
}
