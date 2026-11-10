package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"summermahjong/riichi/internal/game"
)

func sampleResult(t *testing.T) *Result {
	t.Helper()
	cfg, err := (Config{Seeds: []int64{1, 2}, Agents: testAgents(), RoundLimit: 1}).withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	games := []GameResult{
		{
			Seed:        1,
			Rounds:      1,
			Actions:     32,
			FinalScores: [4]int{30000, 25000, 25000, 20000},
			Ranks:       ranksOf([4]int{30000, 25000, 25000, 20000}),
			Seats: [4]SeatRoundStats{
				{Decisions: 8, Wins: 1, WinPoints: 1200},
				{Decisions: 7},
				{Decisions: 9},
				{Decisions: 6, Deals: 1},
			},
			LatenciesMs: [4][]float64{{1.0, 2.0}, {}, {}, {}},
			DurationMs:  12.5,
		},
		{
			Seed:        2,
			Rounds:      1,
			Actions:     28,
			FinalScores: [4]int{20000, 30000, 25000, 25000},
			Ranks:       ranksOf([4]int{20000, 30000, 25000, 25000}),
			Seats: [4]SeatRoundStats{
				{Decisions: 6},
				{Decisions: 8, Wins: 1, WinPoints: 800},
				{Decisions: 7},
				{Decisions: 5},
			},
			DurationMs: 10.0,
		},
	}
	return &Result{
		Config:    cfg,
		Games:     games,
		Summary:   summarize(cfg, games),
		GamesRan:  2,
		StartedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		Duration:  1500 * time.Millisecond,
	}
}

func TestConfigHashStableAndSensitive(t *testing.T) {
	cfg := fastConfig(1, 2)
	normalized, err := cfg.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	first := configHash(normalized)
	if len(first) != 64 {
		t.Fatalf("configHash 应为 64 位十六进制，实际 %q", first)
	}
	if second := configHash(normalized); second != first {
		t.Fatalf("同样配置的 hash 应稳定: %s vs %s", first, second)
	}
	changed := normalized
	changed.Agents = []string{"random", "random", "random", "random"}
	if configHash(changed) == first {
		t.Fatalf("agent 配置变化时 hash 应改变")
	}
}

func TestBuildReportManifest(t *testing.T) {
	report := BuildReport(sampleResult(t), ReportOptions{SeedSpec: "1-2", GitSHA: "deadbeef"})
	manifest := report.Manifest
	if manifest.Tool != ToolVersion {
		t.Fatalf("Tool = %q", manifest.Tool)
	}
	if manifest.GitSHA != "deadbeef" {
		t.Fatalf("GitSHA = %q", manifest.GitSHA)
	}
	if len(manifest.Seeds) != 2 || manifest.Seeds[0] != 1 {
		t.Fatalf("Seeds = %v", manifest.Seeds)
	}
	if manifest.AgentsHash == "" || len(manifest.AgentsHash) != 64 {
		t.Fatalf("AgentsHash = %q", manifest.AgentsHash)
	}
	if manifest.Scorer != "rules" || manifest.Calls != "pass" {
		t.Fatalf("Scorer/Calls = %q/%q", manifest.Scorer, manifest.Calls)
	}
	if manifest.RoundLimit != 1 || manifest.DecisionTimeoutMs <= 0 {
		t.Fatalf("RoundLimit/Timeout = %d/%.1f", manifest.RoundLimit, manifest.DecisionTimeoutMs)
	}
	if _, err := time.Parse(time.RFC3339, manifest.StartedAt); err != nil {
		t.Fatalf("StartedAt 不是 RFC3339: %q", manifest.StartedAt)
	}
	if len(report.Notes) == 0 {
		t.Fatalf("报告应带方法学说明")
	}
	if report.Summary.Games != 2 {
		t.Fatalf("Summary.Games = %d", report.Summary.Games)
	}
}

func TestWriteCreatesThreeFiles(t *testing.T) {
	dir := t.TempDir()
	report := BuildReport(sampleResult(t), ReportOptions{SeedSpec: "1-2", GitSHA: "deadbeef"})
	if err := Write(dir, report); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, name := range []string{"report.json", "manifest.json", "report.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("缺少 %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s 是空文件", name)
		}
	}

	body, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatalf("读 report.json: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("report.json 不是合法 JSON: %v", err)
	}
	if decoded.Manifest.GitSHA != "deadbeef" || len(decoded.Games) != 2 {
		t.Fatalf("report.json 内容不符: %+v", decoded.Manifest)
	}

	manifestBody, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("读 manifest.json: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		t.Fatalf("manifest.json 不是合法 JSON: %v", err)
	}

	markdown, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatalf("读 report.md: %v", err)
	}
	for _, want := range []string{"## 2. 座位指标", "Bradley-Terry", "shanten:attack", "1-2", "seed"} {
		if !strings.Contains(string(markdown), want) {
			t.Fatalf("report.md 缺少 %q", want)
		}
	}
}

func TestMarkdownWithEmptyMetrics(t *testing.T) {
	report := Report{Summary: Summary{}, Manifest: Manifest{Tool: ToolVersion}}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "—") {
		t.Fatalf("空指标应渲染为 —")
	}
	if !strings.Contains(markdown, "## 5. 说明与已知限制") {
		t.Fatalf("缺少说明章节")
	}
}

func TestGitSHA(t *testing.T) {
	sha := GitSHA(context.Background())
	if sha == "" {
		t.Fatalf("GitSHA 不应为空")
	}
	if sha != "unknown" && len(sha) != 40 {
		t.Fatalf("GitSHA 格式异常: %q", sha)
	}
}

type fakeScorer struct{}

func (fakeScorer) ScoreWin(game.WinContext) (game.WinResult, error) {
	return game.WinResult{}, nil
}

func TestScorerName(t *testing.T) {
	if got := scorerName(RulesScorer{}); got != "rules" {
		t.Fatalf("RulesScorer → %q", got)
	}
	if got := scorerName(nil); got != "none" {
		t.Fatalf("nil → %q", got)
	}
	if got := scorerName(fakeScorer{}); !strings.Contains(got, "fakeScorer") {
		t.Fatalf("自定义 scorer → %q", got)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := formatRate(Rate{}); got != "—" {
		t.Fatalf("空比例应渲染为 —，实际 %q", got)
	}
	rate := NewRate(1, 2)
	if got := formatRate(rate); !strings.Contains(got, "%") || !strings.Contains(got, "[") {
		t.Fatalf("比例格式异常: %q", got)
	}
	if got := formatMean(MeanCI{}, 2); got != "—" {
		t.Fatalf("空均值应渲染为 —，实际 %q", got)
	}
	if got := formatMean(NewMeanCI(4, 10, 2), 2); !strings.Contains(got, "[") {
		t.Fatalf("均值格式异常: %q", got)
	}
	if got := shortHash("0123456789abcdef"); got != "0123456789ab…" {
		t.Fatalf("shortHash = %q", got)
	}
	if got := shortHash("short"); got != "short" {
		t.Fatalf("短 hash 不应截断: %q", got)
	}
}
