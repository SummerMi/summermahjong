package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"summermahjong/riichi/internal/game"
)

// ToolVersion 是评测工具版本，写进 manifest 便于对比不同版本的报告。
const ToolVersion = "riichi-eval/0.1.0"

// Manifest 是评测的环境指纹：同样的 manifest + 同样的代码应该得到同样结果。
type Manifest struct {
	Tool              string       `json:"tool"`
	GitSHA            string       `json:"git_sha"`
	GoVersion         string       `json:"go_version"`
	StartedAt         string       `json:"started_at"`
	DurationMs        float64      `json:"duration_ms"`
	SeedSpec          string       `json:"seed_spec"`
	Seeds             []int64      `json:"seeds"`
	Agents            []string     `json:"agents"`
	AgentsHash        string       `json:"agents_hash"`
	Rules             game.Ruleset `json:"rules"`
	RoundLimit        int          `json:"round_limit"`
	DecisionTimeoutMs float64      `json:"decision_timeout_ms"`
	Jobs              int          `json:"jobs"`
	Calls             string       `json:"calls"`
	Scorer            string       `json:"scorer"`
}

// ReportOptions 是构造报告时由调用方（CLI）补充的环境信息。
type ReportOptions struct {
	// SeedSpec 是用户传入的 seed 表达式（如 "1-20"），仅用于 manifest 记录。
	SeedSpec string
	// GitSHA 是代码版本；空串时自动探测（失败记 "unknown"）。
	GitSHA string
	// Notes 是附加到报告里的自定义说明。
	Notes []string
}

// Report 是 report.json 的根结构。
type Report struct {
	Manifest Manifest     `json:"manifest"`
	Summary  Summary      `json:"summary"`
	Games    []GameResult `json:"games"`
	Notes    []string     `json:"notes,omitempty"`
}

// BuildReport 把评测结果打包成报告，并补全 manifest 与说明。
func BuildReport(result *Result, opts ReportOptions) Report {
	cfg := result.Config
	sha := opts.GitSHA
	if sha == "" {
		sha = GitSHA(context.Background())
	}

	manifest := Manifest{
		Tool:              ToolVersion,
		GitSHA:            sha,
		GoVersion:         runtime.Version(),
		StartedAt:         result.StartedAt.UTC().Format(time.RFC3339),
		DurationMs:        float64(result.Duration.Microseconds()) / 1000,
		SeedSpec:          opts.SeedSpec,
		Seeds:             append([]int64(nil), cfg.Seeds...),
		Agents:            append([]string(nil), cfg.Agents...),
		AgentsHash:        configHash(cfg),
		Rules:             cfg.Rules,
		RoundLimit:        cfg.RoundLimit,
		DecisionTimeoutMs: float64(cfg.DecisionTimeout.Microseconds()) / 1000,
		Jobs:              cfg.Jobs,
		Calls:             string(cfg.Calls),
		Scorer:            scorerName(cfg.Scorer),
	}

	report := Report{
		Manifest: manifest,
		Summary:  result.Summary,
		Games:    append([]GameResult(nil), result.Games...),
		Notes:    buildNotes(result, opts.Notes),
	}
	sanitizeReport(&report)
	return report
}

// sanitizeReport 兜底清理报告里的非有限浮点。
//
// 指标结构（Rate/MeanCI/DurationStat）已用 MarshalJSON 把无样本/NaN 输出成 null；
// 这里再清理 manifest / games / ELO 里的裸 float 字段，保证 encoding/json 永不报
// "unsupported value: NaN"。
func sanitizeReport(report *Report) {
	if !isFinite(report.Manifest.DurationMs) {
		report.Manifest.DurationMs = 0
	}
	for i := range report.Games {
		if !isFinite(report.Games[i].DurationMs) {
			report.Games[i].DurationMs = 0
		}
	}
	for name, rating := range report.Summary.Elo.Ratings {
		if !isFinite(rating) {
			report.Summary.Elo.Ratings[name] = 1500
		}
	}
}

// configHash 对影响结果的关键配置做 SHA-256，任何配置变化都会改变 manifest。
func configHash(cfg Config) string {
	payload := struct {
		Agents            []string     `json:"agents"`
		Rules             game.Ruleset `json:"rules"`
		RoundLimit        int          `json:"round_limit"`
		DecisionTimeoutMs float64      `json:"decision_timeout_ms"`
		Calls             string       `json:"calls"`
		Scorer            string       `json:"scorer"`
	}{
		Agents:            cfg.Agents,
		Rules:             cfg.Rules,
		RoundLimit:        cfg.RoundLimit,
		DecisionTimeoutMs: float64(cfg.DecisionTimeout.Microseconds()) / 1000,
		Calls:             string(cfg.Calls),
		Scorer:            scorerName(cfg.Scorer),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "unknown"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// scorerName 给计分实现一个稳定名字（写进 manifest）。
func scorerName(scorer game.ScoreProvider) string {
	switch scorer.(type) {
	case nil:
		return "none"
	case RulesScorer:
		return "rules"
	default:
		return fmt.Sprintf("%T", scorer)
	}
}

// buildNotes 生成报告末尾的方法学说明。
func buildNotes(result *Result, extra []string) []string {
	notes := make([]string, 0, len(extra)+5)
	notes = append(notes,
		fmt.Sprintf("指标口径：和了率/放铳率/立直率/副露率的分母是「该座位打过的轮数」（共 %d 轮）；平均顺位的分母是有效对局数（共 %d 场）。",
			result.Summary.Rounds, result.Summary.Games),
		"置信区间：比例用正态近似 p ± 1.96·√(p(1-p)/n)；均值用样本方差的标准误；样本少时区间仅供参考。",
		"ELO：Bradley-Terry 模型 + MM 迭代（上限 200 轮），按半荘最终点数两两比较（同分各记 0.5），均值平移到 1500。",
		fmt.Sprintf("响应窗口策略：%s（agent = 吃碰杠也由 agent 决定；pass = 只荣和、其余 pass）。", result.Config.Calls),
	)
	var wins, openWins int64
	for _, seat := range result.Summary.Seats {
		wins += seat.Wins
		openWins += seat.OpenWins
	}
	if wins > 0 {
		notes = append(notes, fmt.Sprintf(
			"计分：门清与副露和了都走 rules 真实役/符/点（S5b 起 Decompose 支持副露）；本批副露和了 %d/%d（%.1f%%）。仅当引擎判和而 rules 拆不出/无役时才回退 1 翻 30 符（事件 yaku 会带「近似计分」，可据此核查）。",
			openWins, wins, 100*float64(openWins)/float64(wins),
		))
	}
	if result.Errors > 0 {
		notes = append(notes, fmt.Sprintf(
			"有 %d/%d 场对局因驱动/引擎异常未完成（见 games[].error），这些场次不计入平均顺位。",
			result.Errors, result.Len(),
		))
	}
	notes = append(notes, extra...)
	return notes
}

// Len 返回对局数（辅助方法，避免调用方重复 len）。
func (r *Result) Len() int { return len(r.Games) }

// GitSHA 探测当前仓库 HEAD；失败（非 git 目录/没装 git）返回 "unknown"。
func GitSHA(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "unknown"
	}
	return sha
}

// Write 把 report.json / report.md / manifest.json 写到 outDir。
func Write(outDir string, report Report) error {
	sanitizeReport(&report)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("eval: 创建输出目录: %w", err)
	}
	jsonBody, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("eval: 序列化 report.json: %w", err)
	}
	manifestBody, err := json.MarshalIndent(report.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("eval: 序列化 manifest.json: %w", err)
	}
	files := map[string][]byte{
		"report.json":   append(jsonBody, '\n'),
		"manifest.json": append(manifestBody, '\n'),
		"report.md":     []byte(report.Markdown()),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), body, 0o644); err != nil {
			return fmt.Errorf("eval: 写 %s: %w", name, err)
		}
	}
	return nil
}

// Markdown 渲染人读版报告（表格 + 方法学说明）。
func (r Report) Markdown() string {
	var b strings.Builder
	writeLine := func(format string, args ...any) {
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}

	writeLine("# riichi 自对弈评测报告")
	writeLine("")
	writeLine("## 1. 运行环境与配置")
	writeLine("")
	writeLine("| 项 | 值 |")
	writeLine("|---|---|")
	writeLine("| 工具版本 | %s |", r.Manifest.Tool)
	writeLine("| git sha | `%s` |", r.Manifest.GitSHA)
	writeLine("| Go | %s |", r.Manifest.GoVersion)
	writeLine("| 开始时间（UTC） | %s |", r.Manifest.StartedAt)
	writeLine("| 耗时 | %.1f s |", r.Manifest.DurationMs/1000)
	writeLine("| seed | %s（%d 场） |", r.Manifest.SeedSpec, len(r.Manifest.Seeds))
	writeLine("| agents | `%s` |", strings.Join(r.Manifest.Agents, ", "))
	writeLine("| agents hash | `%s` |", shortHash(r.Manifest.AgentsHash))
	writeLine("| 半荘局数上限 | %d |", r.Manifest.RoundLimit)
	writeLine("| 决策超时 | %.0f ms |", r.Manifest.DecisionTimeoutMs)
	writeLine("| 响应窗口策略 | %s |", r.Manifest.Calls)
	writeLine("| 计分 | %s |", r.Manifest.Scorer)
	writeLine("| 并发 jobs | %d |", r.Manifest.Jobs)
	writeLine("")
	writeLine("## 2. 座位指标（%d 轮 / %d 场）", r.Summary.Rounds, r.Summary.Games)
	writeLine("")
	writeLine("| 座位 | agent | 决策数 | 和了率 | 放铳率 | 平均顺位 | 平均打点 | 立直率 | 副露/轮 | 副露和了占比 | P50/P95 (ms) | 役满 |")
	writeLine("|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, seat := range r.Summary.Seats {
		writeLine("| %d | %s | %d | %s | %s | %s | %s | %s | %s | %s | %.2f / %.2f | %d |",
			seat.Seat, seat.Agent, seat.Decisions,
			formatRate(seat.WinRate), formatRate(seat.DealInRate),
			formatMean(seat.AvgRank, 2), formatMean(seat.AvgWinPts, 0),
			formatRate(seat.RiichiRate), formatRate(seat.MeldRate),
			formatRate(seat.OpenWinRate),
			seat.Decision.P50Ms, seat.Decision.P95Ms, seat.Yakuman)
	}
	writeLine("")
	writeLine("> 和了率/放铳率/立直率/副露率括号内为 95%% 置信区间；平均顺位 1 最好、4 最差。")
	writeLine("")
	writeLine("## 3. ELO（Bradley-Terry）")
	writeLine("")
	writeLine("| 排名 | agent | ELO |")
	writeLine("|---|---|---|")
	for rank, rating := range r.Summary.Elo.SortedRatings() {
		writeLine("| %d | %s | %.1f |", rank+1, rating.Name, rating.Rating)
	}
	writeLine("")
	writeLine("> 迭代 %d 轮（收敛=%v），两两比较 %d 场。",
		r.Summary.Elo.Iterations, r.Summary.Elo.Converged, r.Summary.Elo.Matches)
	writeLine("")
	writeLine("## 4. 对局明细")
	writeLine("")
	writeLine("| seed | 轮数 | 动作数 | 最终点数 | 顺位（按座位） | 耗时 (s) | 备注 |")
	writeLine("|---|---|---|---|---|---|---|")
	for _, g := range r.Games {
		note := g.Error
		if note == "" && g.Aborted {
			note = "aborted"
		}
		writeLine("| %d | %d | %d | %v | %v | %.2f | %s |",
			g.Seed, g.Rounds, g.Actions, g.FinalScores, g.Ranks, g.DurationMs/1000, note)
	}
	writeLine("")
	writeLine("## 5. 说明与已知限制")
	writeLine("")
	for _, note := range r.Notes {
		writeLine("- %s", note)
	}
	return b.String()
}

// shortHash 截断哈希便于表格展示。
func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12] + "…"
}

// formatRate 渲染比例与置信区间（如 "24.3% [18.1, 31.9]"）。
//
// 计数型指标（value > 1，例如每轮副露次数）没有二项置信区间，直接显示为「N 次/轮」；
// 无样本时显示 "—"（JSON 里对应 null）。
func formatRate(rate Rate) string {
	if !rate.HasData() {
		return "—"
	}
	if rate.Value < 0 || rate.Value > 1 {
		return fmt.Sprintf("%.2f 次/轮", rate.Value)
	}
	return fmt.Sprintf("%.1f%% [%.1f, %.1f]", 100*rate.Value, 100*rate.Low, 100*rate.High)
}

// formatMean 渲染均值与置信区间。
func formatMean(stat MeanCI, digits int) string {
	if stat.N == 0 {
		return "—"
	}
	format := "%." + fmt.Sprint(digits) + "f [%." + fmt.Sprint(digits) + "f, %." + fmt.Sprint(digits) + "f]"
	return fmt.Sprintf(format, stat.Mean, stat.Low, stat.High)
}
