// Command riichi-eval 是 riichi 的进程内自对弈评测入口（S9）。
//
// 用法示例：
//
//	# 默认：seed 1-100，四家分别为 shanten:attack / defense:defense / search:closed / random
//	go run ./cmd/riichi-eval
//
//	# 只跑 20 场，输出到 .tmp-eval
//	go run ./cmd/riichi-eval --count 20 --out .tmp-eval
//
//	# 指定 seed 区间与 agent 组合，4 并发
//	go run ./cmd/riichi-eval --seeds 1-50,100 --agents random,random,random,random --jobs 4
//
// 注意：本工具**不走网络**，直接 game.New/Apply 在进程内驱动；输出目录（默认
// ./eval-out）不应提交到仓库（.tmp-eval 已在 .git/info/exclude 中忽略）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/agent/llm"
	"summermahjong/riichi/internal/eval"
	"summermahjong/riichi/internal/game"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "riichi-eval: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		seedsSpec   = flag.String("seeds", "1-100", "seed 表达式，如 1-100 / 1,2,5-7")
		count       = flag.Int("count", 0, "只跑前 N 个 seed（>0 时覆盖 --seeds，等价于 seeds 1-N）")
		agentsSpec  = flag.String("agents", "shanten:attack,defense:defense,search:closed,random", "四个座位的 agent，逗号分隔")
		outDir      = flag.String("out", "./eval-out", "报告输出目录（report.json / report.md / manifest.json）")
		jobs        = flag.Int("jobs", min(runtime.NumCPU(), 4), "并发跑不同 seed 的 goroutine 数")
		timeout     = flag.Duration("timeout", eval.DefaultDecisionTimeout, "单次决策上限（超时回退启发式首选）")
		roundLimit  = flag.Int("round-limit", eval.DefaultRoundLimit, "每场对局的半荘局数上限（1 = 只打一局）")
		calls       = flag.String("calls", string(eval.CallsPass), "响应窗口策略：pass（只荣和，默认；S5 副露后打牌未接通）/ agent（全部交给 agent，会产生 aborted）")
		redFives    = flag.Int("red-fives", 3, "赤宝牌数量 0-3")
		startPoints = flag.Int("starting-points", 25000, "起始点数")

		llmMock    = flag.Bool("llm-mock", false, "llm 座位用 MockClient（不联网，只验证评测链路）")
		llmBaseURL = flag.String("llm-base-url", "", "OpenAI 兼容 base URL（缺省读 LLM_BASE_URL/OPENAI_BASE_URL）")
		llmModel   = flag.String("llm-model", "", "模型名（缺省读 LLM_MODEL/OPENAI_MODEL）")
		llmAPIKey  = flag.String("llm-api-key", "", "API key（缺省读 LLM_API_KEY/OPENAI_API_KEY）")
		llmTimeout = flag.Duration("llm-timeout", 2*time.Second, "单次 LLM 调用超时")
		llmTopK    = flag.Int("llm-topk", 5, "交给 LLM 的候选数")
	)
	flag.Parse()

	seeds, spec, err := parseSeeds(*seedsSpec, *count)
	if err != nil {
		return err
	}
	agents := splitAgents(*agentsSpec)
	if len(agents) != 4 {
		return fmt.Errorf("--agents 必须恰好 4 个（当前 %d 个: %v）", len(agents), agents)
	}

	var (
		llmAgents []*llm.Agent
		llmMu     sync.Mutex
	)
	builder := func(name string, seed int64) (agent.Agent, error) {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "llm") {
			return agent.New(name, agent.Options{Seed: seed})
		}
		var client llm.Client
		if *llmMock {
			client = &llm.MockClient{}
		} else {
			key := *llmAPIKey
			if key == "" {
				key = firstEnv("LLM_API_KEY", "OPENAI_API_KEY")
			}
			if key == "" {
				return nil, fmt.Errorf("llm: 缺少 API key（-llm-api-key / LLM_API_KEY / OPENAI_API_KEY）；可先用 -llm-mock 验证链路")
			}
			base := *llmBaseURL
			if base == "" {
				base = firstEnv("LLM_BASE_URL", "OPENAI_BASE_URL")
			}
			model := *llmModel
			if model == "" {
				model = firstEnv("LLM_MODEL", "OPENAI_MODEL")
			}
			client = llm.NewOpenAICompatClient(base, key, model)
		}
		instance := llm.New(client, llm.WithTimeout(*llmTimeout), llm.WithTopK(*llmTopK))
		llmMu.Lock()
		llmAgents = append(llmAgents, instance)
		llmMu.Unlock()
		return instance, nil
	}

	cfg := eval.Config{
		Seeds:           seeds,
		Agents:          agents,
		Rules:           game.DefaultRuleset(),
		RoundLimit:      *roundLimit,
		DecisionTimeout: *timeout,
		Jobs:            *jobs,
		Calls:           eval.CallsPolicy(*calls),
		AgentBuilder:    builder,
	}
	cfg.Rules.RedFives = *redFives
	cfg.Rules.StartingPoints = *startPoints

	fmt.Printf("riichi-eval: %d 场（每场最多 %d 局），agents=%v，jobs=%d，calls=%s\n",
		len(cfg.Seeds), cfg.RoundLimit, cfg.Agents, cfg.Jobs, cfg.Calls)
	fmt.Printf("seed: %s\n", spec)
	started := time.Now()

	result, err := eval.Run(context.Background(), cfg)
	if err != nil {
		return err
	}
	report := eval.BuildReport(result, eval.ReportOptions{SeedSpec: spec})
	if err := eval.Write(*outDir, report); err != nil {
		return err
	}
	printSummary(report, *outDir)
	if len(llmAgents) > 0 {
		var total llm.Stats
		for _, a := range llmAgents {
			st := a.Stats()
			total.Calls += st.Calls
			total.Fallbacks += st.Fallbacks
			total.Invalid += st.Invalid
			total.Errors += st.Errors
			total.PromptTokens += st.PromptTokens
			total.CompletionTokens += st.CompletionTokens
		}
		fmt.Printf("LLM 统计：agents=%d model=%s calls=%d fallbacks=%d errors=%d invalid=%d prompt_tokens=%d completion_tokens=%d\n",
			len(llmAgents), llmAgents[0].Model(), total.Calls, total.Fallbacks, total.Errors,
			total.Invalid, total.PromptTokens, total.CompletionTokens)
	}

	if result.Errors == len(result.Games) && len(result.Games) > 0 {
		return fmt.Errorf("全部 %d 场对局都失败了，请检查上面的错误信息", result.Errors)
	}
	fmt.Printf("总耗时 %.2fs（含写报告）\n", time.Since(started).Seconds())
	return nil
}

// parseSeeds 解析 seed 表达式；count>0 时优先，返回 (seeds, 可读的 spec 描述)。
//
// 支持 "1-100"、"1,2,5-7"、"0-9,42"，空白容忍；区间两端含。重复 seed 会被去掉。
func parseSeeds(spec string, count int) ([]int64, string, error) {
	if count > 0 {
		seeds := make([]int64, 0, count)
		for i := 1; i <= count; i++ {
			seeds = append(seeds, int64(i))
		}
		return seeds, fmt.Sprintf("1-%d（--count）", count), nil
	}
	cleaned := strings.ReplaceAll(spec, " ", "")
	if cleaned == "" {
		return nil, "", fmt.Errorf("--seeds 为空")
	}
	seen := make(map[int64]struct{})
	seeds := make([]int64, 0, 16)
	for _, part := range strings.Split(cleaned, ",") {
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			if len(bounds) != 2 || bounds[0] == "" || bounds[1] == "" {
				return nil, "", fmt.Errorf("非法 seed 区间 %q", part)
			}
			start, err := strconv.ParseInt(bounds[0], 10, 64)
			if err != nil {
				return nil, "", fmt.Errorf("非法 seed 区间 %q: %w", part, err)
			}
			end, err := strconv.ParseInt(bounds[1], 10, 64)
			if err != nil {
				return nil, "", fmt.Errorf("非法 seed 区间 %q: %w", part, err)
			}
			if end < start {
				return nil, "", fmt.Errorf("seed 区间 %q 的起点大于终点", part)
			}
			if end-start > 1_000_000 {
				return nil, "", fmt.Errorf("seed 区间 %q 太大（上限 100 万）", part)
			}
			for seed := start; seed <= end; seed++ {
				if _, ok := seen[seed]; ok {
					continue
				}
				seen[seed] = struct{}{}
				seeds = append(seeds, seed)
			}
			continue
		}
		seed, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("非法 seed %q: %w", part, err)
		}
		if _, ok := seen[seed]; ok {
			continue
		}
		seen[seed] = struct{}{}
		seeds = append(seeds, seed)
	}
	if len(seeds) == 0 {
		return nil, "", fmt.Errorf("--seeds 没有解析出任何 seed")
	}
	return seeds, cleaned, nil
}

// splitAgents 解析逗号分隔的 agent 列表（去空白）。
func splitAgents(spec string) []string {
	parts := strings.Split(spec, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// printSummary 把关键指标打到 stdout（完整表格见 report.md）。
func printSummary(report eval.Report, outDir string) {
	fmt.Println()
	fmt.Printf("%-4s %-22s %8s %18s %18s %16s %12s %10s\n",
		"座位", "agent", "决策数", "和了率", "放铳率", "平均顺位", "立直率", "副露/轮")
	for _, seat := range report.Summary.Seats {
		fmt.Printf("%-6d %-22s %8d %18s %18s %16s %12s %10s\n",
			seat.Seat, seat.Agent, seat.Decisions,
			formatRate(seat.WinRate), formatRate(seat.DealInRate),
			formatMean(seat.AvgRank), formatRate(seat.RiichiRate), formatRate(seat.MeldRate))
	}
	fmt.Println()
	fmt.Println("ELO（Bradley-Terry）：")
	for rank, rating := range report.Summary.Elo.SortedRatings() {
		fmt.Printf("  %d. %-24s %.1f\n", rank+1, rating.Name, rating.Rating)
	}
	fmt.Println()
	fmt.Printf("报告已写入 %s/{report.json,report.md,manifest.json}；共 %d 场 / %d 轮，失败 %d 场\n",
		outDir, report.Summary.Games, report.Summary.Rounds, report.Summary.ErrorN)
}

// formatRate 渲染比例与 95% 置信区间；计数型指标（>1）显示为「N 次/轮」。
func formatRate(rate eval.Rate) string {
	if !rate.HasData() {
		return "—"
	}
	if rate.Value < 0 || rate.Value > 1 {
		return fmt.Sprintf("%.2f 次/轮", rate.Value)
	}
	return fmt.Sprintf("%.1f%% [%.1f,%.1f]", 100*rate.Value, 100*rate.Low, 100*rate.High)
}

// formatMean 渲染均值与 95% 置信区间。
func formatMean(stat eval.MeanCI) string {
	if !stat.HasData() {
		return "—"
	}
	return fmt.Sprintf("%.2f [%.2f,%.2f]", stat.Mean, stat.Low, stat.High)
}

// firstEnv 返回第一个非空环境变量（供 LLM 配置缺省值）。
func firstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
