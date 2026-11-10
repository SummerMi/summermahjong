package transport

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// latencySampleCap 是决策耗时环形样本上限：只保留最近 N 个样本算分位数，
// 内存恒定，长局也不会无限增长（全量统计由 Count/Total 原子计数保证）。
const latencySampleCap = 512

// latencyRecorder 记录决策耗时并计算 P50/P95（不引入 Prometheus 依赖）。
type latencyRecorder struct {
	mu      sync.Mutex
	samples []time.Duration
	count   int64
	total   time.Duration
}

// observe 记录一次耗时样本。
func (l *latencyRecorder) observe(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count++
	l.total += d
	l.samples = append(l.samples, d)
	if len(l.samples) > latencySampleCap {
		// 丢弃最旧的样本，保持环形容量
		l.samples = append(l.samples[:0], l.samples[len(l.samples)-latencySampleCap:]...)
	}
}

// latencySnapshot 是耗时统计快照（毫秒）。
type latencySnapshot struct {
	Count int64   `json:"count"`
	AvgMS float64 `json:"avg_ms"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	MaxMS float64 `json:"max_ms"`
}

// snapshot 返回耗时统计（复制样本后排序，避免持锁计算）。
func (l *latencyRecorder) snapshot() latencySnapshot {
	l.mu.Lock()
	samples := make([]time.Duration, len(l.samples))
	copy(samples, l.samples)
	count := l.count
	total := l.total
	l.mu.Unlock()

	out := latencySnapshot{Count: count}
	if len(samples) == 0 {
		return out
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	out.P50MS = durationMS(percentile(samples, 0.50))
	out.P95MS = durationMS(percentile(samples, 0.95))
	out.MaxMS = durationMS(samples[len(samples)-1])
	if count > 0 {
		out.AvgMS = durationMS(total / time.Duration(count))
	}
	return out
}

// percentile 取最近秩分位数（样本已升序）。
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * p)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// durationMS 把 Duration 转成毫秒浮点数（保留两位小数便于阅读）。
func durationMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// metrics 是进程级指标（HTTP /metrics 输出）。
//
// 用原子计数而不是锁：写路径是每事件一次，读路径是运维偶尔拉取，原子计数最省。
type metrics struct {
	startedAt time.Time

	gamesCreated        atomic.Int64
	gamesActive         atomic.Int64
	gamesClosed         atomic.Int64
	wsConnectionsTotal  atomic.Int64
	wsConnectionsActive atomic.Int64
	wsResumes           atomic.Int64
	actionsApplied      atomic.Int64
	eventsPersisted     atomic.Int64
	agentDecisions      atomic.Int64
	agentFallbacks      atomic.Int64
	errorsTotal         atomic.Int64
	latency             latencyRecorder
}

// newMetrics 构造进程指标。
func newMetrics() *metrics {
	return &metrics{startedAt: time.Now()}
}

// metricsSnapshot 是 /metrics 的 JSON 结构。
type metricsSnapshot struct {
	UptimeSeconds       float64         `json:"uptime_seconds"`
	GamesCreated        int64           `json:"games_created"`
	GamesActive         int64           `json:"games_active"`
	GamesClosed         int64           `json:"games_closed"`
	WSConnectionsTotal  int64           `json:"ws_connections_total"`
	WSConnectionsActive int64           `json:"ws_connections_active"`
	WSResumes           int64           `json:"ws_resumes"`
	ActionsApplied      int64           `json:"actions_applied"`
	EventsPersisted     int64           `json:"events_persisted"`
	AgentDecisions      int64           `json:"agent_decisions"`
	AgentFallbacks      int64           `json:"agent_fallbacks"`
	ErrorsTotal         int64           `json:"errors_total"`
	DecisionLatency     latencySnapshot `json:"decision_latency"`
}

// snapshot 返回进程指标快照。
func (m *metrics) snapshot() metricsSnapshot {
	return metricsSnapshot{
		UptimeSeconds:       time.Since(m.startedAt).Seconds(),
		GamesCreated:        m.gamesCreated.Load(),
		GamesActive:         m.gamesActive.Load(),
		GamesClosed:         m.gamesClosed.Load(),
		WSConnectionsTotal:  m.wsConnectionsTotal.Load(),
		WSConnectionsActive: m.wsConnectionsActive.Load(),
		WSResumes:           m.wsResumes.Load(),
		ActionsApplied:      m.actionsApplied.Load(),
		EventsPersisted:     m.eventsPersisted.Load(),
		AgentDecisions:      m.agentDecisions.Load(),
		AgentFallbacks:      m.agentFallbacks.Load(),
		ErrorsTotal:         m.errorsTotal.Load(),
		DecisionLatency:     m.latency.snapshot(),
	}
}

// roomMetrics 是单局指标（GET /api/v1/games/{id}/metrics 输出）。
type roomMetrics struct {
	eventsPersisted atomic.Int64
	actionsApplied  atomic.Int64
	agentDecisions  atomic.Int64
	agentFallbacks  atomic.Int64
	latency         latencyRecorder
}

// roomMetricsSnapshot 是单局指标 JSON 结构。
type roomMetricsSnapshot struct {
	GameID          string          `json:"game_id"`
	Seq             uint64          `json:"seq"`
	Phase           string          `json:"phase"`
	WallRemaining   int             `json:"wall_remaining"`
	EventsPersisted int64           `json:"events_persisted"`
	ActionsApplied  int64           `json:"actions_applied"`
	AgentDecisions  int64           `json:"agent_decisions"`
	AgentFallbacks  int64           `json:"agent_fallbacks"`
	DecisionLatency latencySnapshot `json:"decision_latency"`
}
