package eval

import (
	"math"
	"testing"
)

func TestNewPairwiseDeduplicatesNames(t *testing.T) {
	p := NewPairwise([]string{"a", "a", "b", "c"})
	names := p.Names()
	want := []string{"a", "b", "c"}
	if len(names) != len(want) {
		t.Fatalf("names = %v want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v want %v", names, want)
		}
	}
}

func TestPairwiseAddScores(t *testing.T) {
	p := NewPairwise([]string{"a", "b", "c", "d"})
	p.AddScores([]string{"a", "b", "c", "d"}, [4]int{30000, 20000, 10000, 5000})
	// a 赢 b/c/d 各一次
	if p.Wins(0, 1) != 1 || p.Games(0, 1) != 1 {
		t.Fatalf("a vs b: wins=%v games=%v", p.Wins(0, 1), p.Games(0, 1))
	}
	if p.Wins(3, 0) != 0 {
		t.Fatalf("d 不应赢 a: %v", p.Wins(3, 0))
	}

	// 平分：各记 0.5
	p.AddScores([]string{"a", "b", "c", "d"}, [4]int{25000, 25000, 25000, 25000})
	if p.Wins(0, 1) != 1.5 {
		t.Fatalf("并列后 a vs b 胜场应为 1.5，实际 %v", p.Wins(0, 1))
	}
	if p.Matches() != 12 {
		t.Fatalf("Matches = %d want 12", p.Matches())
	}
}

func TestRatingsOrderingAndScale(t *testing.T) {
	p := NewPairwise([]string{"a", "b", "c", "d"})
	// 10 场固定顺位 a>b>c>d
	for i := 0; i < 10; i++ {
		p.AddScores([]string{"a", "b", "c", "d"}, [4]int{40000, 30000, 20000, 10000})
	}
	result := p.Ratings(200)
	ratings := result.Ratings
	if !(ratings["a"] > ratings["b"] && ratings["b"] > ratings["c"] && ratings["c"] > ratings["d"]) {
		t.Fatalf("ELO 顺序错误: %+v", ratings)
	}
	// 均值应被平移到 1500
	sum := 0.0
	for _, rating := range ratings {
		sum += rating
	}
	approxEqual(t, sum/4, 1500, 0.2, "ELO 均值")
	if result.Iterations <= 0 || result.Iterations > 200 {
		t.Fatalf("迭代轮数异常: %d", result.Iterations)
	}
	if result.Matches != 60 {
		t.Fatalf("Matches = %d want 60", result.Matches)
	}
}

func TestRatingsAllTiesAreEqual(t *testing.T) {
	p := NewPairwise([]string{"a", "b", "c", "d"})
	for i := 0; i < 5; i++ {
		p.AddScores([]string{"a", "b", "c", "d"}, [4]int{25000, 25000, 25000, 25000})
	}
	result := p.Ratings(200)
	for name, rating := range result.Ratings {
		approxEqual(t, rating, 1500, 0.2, "全部同分时 "+name+" 应为 1500")
	}
}

func TestRatingsEdgeCases(t *testing.T) {
	// 空名单
	empty := NewPairwise(nil).Ratings(200)
	if len(empty.Ratings) != 0 || !empty.Converged {
		t.Fatalf("空名单应返回空且收敛: %+v", empty)
	}
	// 单 agent
	single := NewPairwise([]string{"solo"}).Ratings(200)
	if single.Ratings["solo"] != 1500 {
		t.Fatalf("单 agent 应为 1500: %+v", single.Ratings)
	}
	// 无对局：所有人为 1500
	noGames := NewPairwise([]string{"a", "b"}).Ratings(200)
	for _, rating := range noGames.Ratings {
		if math.Abs(rating-1500) > 1e-9 {
			t.Fatalf("无对局时应为 1500: %+v", noGames.Ratings)
		}
	}
	// iterations <= 0 走默认 200
	defaulted := NewPairwise([]string{"a", "b"}).Ratings(0)
	if defaulted.Iterations <= 0 {
		t.Fatalf("iterations 默认值未生效: %+v", defaulted)
	}
}

func TestSortedRatings(t *testing.T) {
	p := NewPairwise([]string{"a", "b"})
	p.AddScores([]string{"a", "b", "a", "b"}, [4]int{30000, 10000, 30000, 10000})
	sorted := p.Ratings(200).SortedRatings()
	if len(sorted) != 2 || sorted[0].Rating < sorted[1].Rating {
		t.Fatalf("SortedRatings 应降序: %+v", sorted)
	}
	if sorted[0].Name != "a" {
		t.Fatalf("a 应排第一: %+v", sorted)
	}
}
