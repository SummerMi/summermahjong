package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
	"summermahjong/riichi/internal/tile"
)

const (
	// clientSendBuffer 是单连接发送缓冲。满了说明客户端消费不过来（慢客户端），
	// 房间会摘除该连接而不是阻塞其它玩家。
	clientSendBuffer = 256
	// actionIDCacheLimit 是幂等去重表的容量上限（每局动作数远小于此值）。
	actionIDCacheLimit = 4096
	// defaultAgentTimeout 是 Agent 决策超时兜底（契约 §4：默认 500ms）。
	defaultAgentTimeout = 500 * time.Millisecond
	// defaultResponseTimeout 是人类响应窗口默认超时（超时按 pass 处理）。
	defaultResponseTimeout = 5 * time.Second
)

// roomSnapshot 是不可变的对局快照，供 HTTP 查询无锁读取。
//
// 为什么存 Observation 数组而不是 *game.State：State 会被 actor goroutine 持续修改，
// 直接读会产生数据竞争；Observe 会深拷贝手牌/牌河/副露，快照一旦发布就不再变化。
type roomSnapshot struct {
	seq           uint64
	phase         game.Phase
	turn          game.Seat
	wallRemaining int
	observations  [seatCount]game.Observation
}

// client 是一条 WS 连接在服务端的句柄。
//
// send / done 供多个 goroutine 使用；synced 与 lastSentSeq 只由房间 actor 读写。
type client struct {
	seat      game.Seat
	spectator bool // 观战连接：只读，不接收响应选项、不能提交动作
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once

	synced      bool   // resume 完成后才开始接收实时事件，避免与补齐事件重复
	lastSentSeq uint64 // 已发送给该连接的最大事件 seq（actor 专用）
}

// newClient 创建连接句柄。
func newClient(seat game.Seat) *client {
	return &client{
		seat: seat,
		send: make(chan []byte, clientSendBuffer),
		done: make(chan struct{}),
	}
}

// newSpectatorClient 创建观战连接句柄（只读）。
func newSpectatorClient() *client {
	return &client{
		spectator: true,
		send:      make(chan []byte, clientSendBuffer),
		done:      make(chan struct{}),
	}
}

// close 关闭连接（幂等）。
func (c *client) close() {
	c.closeOnce.Do(func() { close(c.done) })
}

// trySend 非阻塞投递一条消息；缓冲满或连接已关闭返回 false。
func (c *client) trySend(data []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- data:
		return true
	default:
		return false
	}
}

// sendMessage 序列化并投递消息。
func (c *client) sendMessage(msg any) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	return c.trySend(data)
}

// sendError 投递协议错误消息（连接不断开）。
func (c *client) sendError(code, message string) {
	c.sendMessage(errorMessage{Type: "error", Code: code, Message: message})
}

// actionRequest 是提交给房间 actor 的动作请求。
type actionRequest struct {
	seat     game.Seat
	action   game.Action
	actionID string
	seq      uint64
	reply    chan error
}

// attachRequest 是注册订阅者的请求。
type attachRequest struct {
	client *client
	done   chan struct{}
}

// resumeRequest 是断线续传请求。
type resumeRequest struct {
	client  *client
	lastSeq uint64
	reply   chan error
}

// aiResult 是 AI 决策 goroutine 回传的结果。
//
// response=true 表示这是响应窗口里的决策（ron/pon/chi/kan/pass），
// false 表示自回合的打牌/立直/杠/自摸决策。
type aiResult struct {
	seat     game.Seat
	action   game.Action
	elapsed  time.Duration
	fallback bool
	response bool
}

// roomConfig 是房间的静态配置。
type roomConfig struct {
	store        store.Store
	agents       [seatCount]agent.Agent // nil 表示人类座位（等 WS 动作）
	agentNames   [seatCount]string
	agentTimeout time.Duration
	// manualResponses=true 时对局使用显式响应窗口（game.Options.ManualResponses）：
	// 打牌/加杠后推 responses 给人类座位、AI 自动决策；false 表示兼容模式（全部自动 pass）。
	manualResponses bool
	// responseTimeout 是人类响应窗口超时（超时按 pass 结算）。
	responseTimeout time.Duration
	// autoAdvanceRounds=true 时，本局结束（round_end）后自动 StartNextRound，
	// 直到南 4 结束/トビ終了（game_end）；false 则停在 round_end（单局调试）。
	autoAdvanceRounds bool
	logger            *slog.Logger
	metrics           *metrics // 可为 nil（单测直接建房间时）
}

// Room 是一局对局的 actor：单 goroutine 串行执行「Apply → 落库 → 广播 → AI 驱动」。
//
// 为什么用 actor 而不是给状态加锁：对局状态迁移必须严格串行（事件 seq 单调），
// 单 goroutine + channel 把并发收敛到一个点，既没有锁顺序问题，也天然保证
// 「先落库再广播」的因果顺序。
type Room struct {
	id   string
	meta store.GameMeta
	cfg  roomConfig

	st *game.State

	actionCh chan *actionRequest
	attachCh chan *attachRequest
	resumeCh chan *resumeRequest
	aiCh     chan aiResult
	quit     chan struct{}

	closeOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc

	subsMu sync.Mutex
	subs   map[*client]struct{}

	snap    atomic.Pointer[roomSnapshot]
	metrics roomMetrics

	// 以下字段只有 actor goroutine 访问
	aiBusy             bool
	lastSavedSeq       uint64
	processedActionIDs map[string]struct{}
	actionIDOrder      []string

	// 响应窗口状态（actor 专用）
	responseActive bool            // 当前窗口是否已调度（推送/AI/定时器）
	responseAI     [seatCount]bool // 已启动响应决策的 AI 座位（防止重复启动）
	responseTimer  *time.Timer     // 当前窗口的超时定时器
	responseGen    int             // 窗口代次：旧定时器回传时用于丢弃过期超时
	timeoutCh      chan int        // 超时信号（携带窗口代次）
}

// newRoom 构造房间（纯内存初始化，不启动 goroutine、不落库）。
//
// 调用方随后调用 persistInitial 落库，再 Start 启动 actor。
func newRoom(id string, st *game.State, meta store.GameMeta, cfg roomConfig) *Room {
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	if cfg.agentTimeout <= 0 {
		cfg.agentTimeout = defaultAgentTimeout
	}
	if cfg.responseTimeout <= 0 {
		cfg.responseTimeout = defaultResponseTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Room{
		id:                 id,
		meta:               meta,
		cfg:                cfg,
		st:                 st,
		actionCh:           make(chan *actionRequest, 16),
		attachCh:           make(chan *attachRequest, 16),
		resumeCh:           make(chan *resumeRequest, 16),
		aiCh:               make(chan aiResult, 4),
		timeoutCh:          make(chan int, 4),
		quit:               make(chan struct{}),
		ctx:                ctx,
		cancel:             cancel,
		subs:               make(map[*client]struct{}),
		processedActionIDs: make(map[string]struct{}),
	}
	// 补全初始事件（M1 只有 round_start 一条）的 payload
	for i := range r.st.Events {
		enrichEvent(r.st, &r.st.Events[i], false)
	}
	r.refreshSnapshot()
	return r
}

// ID 返回对局 id。
func (r *Room) ID() string { return r.id }

// Meta 返回对局元数据。
func (r *Room) Meta() store.GameMeta { return r.meta }

// Agents 返回四家策略名。
func (r *Room) Agents() []string {
	out := make([]string, seatCount)
	copy(out[:], r.cfg.agentNames[:])
	return out
}

// Snapshot 返回最近一次发布的不可变快照（可能为 nil，仅构造期）。
func (r *Room) Snapshot() *roomSnapshot { return r.snap.Load() }

// persistInitial 把初始事件（round_start）写入 store。
func (r *Room) persistInitial(ctx context.Context) error {
	if err := r.cfg.store.AppendEvents(ctx, r.id, r.st.Events); err != nil {
		return fmt.Errorf("append initial events: %w", err)
	}
	r.lastSavedSeq = r.st.Seq
	r.metrics.eventsPersisted.Add(int64(len(r.st.Events)))
	if r.cfg.metrics != nil {
		r.cfg.metrics.eventsPersisted.Add(int64(len(r.st.Events)))
	}
	return nil
}

// Start 启动房间 actor：先同步推进首个回合，再进入常驻循环。
//
// 同步推进首个回合的目的是让建局响应里的 seq/状态与首次事件一致（可预测），
// 避免「刚建完局读到 seq=1、立刻又被后台推到 seq=2」的偶发差异。
func (r *Room) Start() {
	r.drive()
	go r.run()
}

// Close 停止房间：关闭所有连接、取消 AI goroutine、退出 actor。
func (r *Room) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		close(r.quit)
		if r.responseTimer != nil {
			r.responseTimer.Stop()
		}
		r.subsMu.Lock()
		for c := range r.subs {
			c.close()
		}
		r.subs = make(map[*client]struct{})
		r.subsMu.Unlock()
	})
}

// run 是 actor 主循环：所有状态变更都在这里串行执行。
func (r *Room) run() {
	for {
		select {
		case <-r.quit:
			return
		case req := <-r.actionCh:
			r.handleAction(req)
		case req := <-r.attachCh:
			r.handleAttach(req)
		case req := <-r.resumeCh:
			r.handleResume(req)
		case res := <-r.aiCh:
			r.handleAI(res)
		case gen := <-r.timeoutCh:
			r.handleResponseTimeout(gen)
		}
	}
}

// drive 推进「无需人工决策」的状态：自动摸牌、驱动 AI 打牌、调度响应窗口。
//
// 人类座位进入 AwaitDiscard / 待响应时返回，等待 WS action 消息或超时。
func (r *Room) drive() {
	for {
		switch r.st.Phase {
		case game.PhaseAwaitDraw:
			// 摸牌没有选择余地：由服务端自动执行（人类/AI 一致），事件照常广播
			if err := r.applyAction(game.Action{Kind: game.ActDraw, Seat: r.st.Turn}); err != nil {
				if r.st.Phase == game.PhaseRoundEnd {
					// 荒牌流局：事件已在 applyAction 里落库/广播；按开关继续开下一局
					if !r.advanceRound() {
						return
					}
					continue
				}
				if r.st.Phase == game.PhaseGameEnd {
					return
				}
				r.cfg.logger.Info("对局停止推进",
					"game_id", r.id, "seq", r.st.Seq, "reason", err)
				return
			}
		case game.PhaseAwaitDiscard:
			seat := r.st.Turn
			if r.cfg.agents[seat] == nil {
				return // 人类回合：等 WS 动作
			}
			if r.aiBusy {
				return
			}
			r.aiBusy = true
			r.launchAI(seat)
			return
		case game.PhaseAwaitResponse:
			// 响应窗口：把选项推给人类座位 / 启动 AI 决策 / 启动超时定时器，然后等消息
			r.startResponseWindow()
			return
		case game.PhaseRoundEnd:
			// 自动开下一局（Config.AutoAdvanceRounds，默认 true）：整场连打。
			if !r.advanceRound() {
				return
			}
			continue // 新一局回到 await_draw，继续自动摸牌
		default:
			return
		}
	}
}

// advanceRound 在 round_end 后自动开下一局，并把新事件落库/广播。
//
// 返回 true 表示已进入下一局（调用方继续驱动）；false 表示终局（game_end）或
// 该房间关闭了自动推进（单局调试）。
func (r *Room) advanceRound() bool {
	if !r.cfg.autoAdvanceRounds {
		return false
	}
	seqBefore := r.st.Seq
	if err := r.st.StartNextRound(); err != nil {
		r.cfg.logger.Error("自动开下一局失败",
			"game_id", r.id, "seq", r.st.Seq, "err", err)
		return false
	}
	// round_end 已经在上一手广播过；这里广播新一局的 round_start（或终局 game_end）
	r.afterApply(seqBefore, false)
	return r.st.Phase != game.PhaseGameEnd
}

// launchAI 在独立 goroutine 里跑 Agent，带 deadline；超时/异常回退到首个合法动作。
func (r *Room) launchAI(seat game.Seat) {
	obs := r.st.Observe(seat)
	ag := r.cfg.agents[seat]
	go func() {
		started := time.Now()
		ctx, cancel := context.WithTimeout(r.ctx, r.cfg.agentTimeout)
		defer cancel()

		action, err := ag.Decide(ctx, obs)
		elapsed := time.Since(started)
		fallback := err != nil || ctx.Err() != nil || !agent.InLegalActions(obs, action)
		if fallback {
			if len(obs.LegalActions) == 0 {
				return // 没有合法动作：让房间停住，由上层状态机决定
			}
			action = obs.LegalActions[0]
		}
		select {
		case r.aiCh <- aiResult{seat: seat, action: action, elapsed: elapsed, fallback: fallback}:
		case <-r.quit:
		}
	}()
}

// handleAI 处理 AI 决策结果：响应窗口决策交给 handleAIResponse，自回合决策走原逻辑。
func (r *Room) handleAI(res aiResult) {
	if res.response {
		r.handleAIResponse(res)
		return
	}
	r.aiBusy = false
	if r.st.Phase != game.PhaseAwaitDiscard || r.st.Turn != res.seat {
		return // 结果已过期（对局已推进），丢弃
	}
	obs := r.st.Observe(res.seat)
	action := res.action
	if res.fallback || !agent.InLegalActions(obs, action) {
		if len(obs.LegalActions) == 0 {
			return
		}
		action = obs.LegalActions[0]
		res.fallback = true
	}
	if err := r.applyAction(action); err != nil {
		r.cfg.logger.Warn("AI 动作执行失败，回退到首个合法动作",
			"game_id", r.id, "seat", res.seat, "err", err)
		if legal := r.st.Legal(); len(legal) > 0 {
			_ = r.applyAction(legal[0])
		}
	}
	r.publishAgentThought(res.seat, action, res.elapsed, obs)
	r.recordDecision(res)
	r.drive()
}

// ── 响应窗口（task-14）────────────────────────────────────────────

// startResponseWindow 调度一次响应窗口：
//   - AI 座位：启动独立决策 goroutine（obs.LegalActions 已是该座位的响应选项）；
//   - 人类座位：把 {"type":"responses",...} 推给其所有已 resume 的连接；
//   - 同时启动一个窗口级超时定时器，超时后所有未响应座位按 pass 结算。
//
// 幂等：drive() 会被多次调用，用 responseActive 保证同一个窗口只调度一次。
func (r *Room) startResponseWindow() {
	if r.st.Phase != game.PhaseAwaitResponse || r.st.Pending == nil {
		return
	}
	if r.responseActive {
		return
	}
	r.responseActive = true
	r.responseGen++
	gen := r.responseGen
	r.responseTimer = time.AfterFunc(r.cfg.responseTimeout, func() {
		select {
		case r.timeoutCh <- gen:
		case <-r.quit:
		}
	})

	for seat := game.Seat(0); seat < seatCount; seat++ {
		if r.st.Pending.Decided[seat] {
			continue
		}
		if r.cfg.agents[seat] != nil {
			if r.responseAI[seat] {
				continue
			}
			r.responseAI[seat] = true
			r.launchResponseAI(seat)
			continue
		}
		r.pushResponses(seat)
	}
}

// pushResponses 把一个座位的响应选项推给该座位的所有已同步连接。
func (r *Room) pushResponses(seat game.Seat) {
	options := r.st.Responses(seat)
	if len(options) == 0 {
		return
	}
	wire := make([]actionWire, 0, len(options))
	for _, action := range options {
		wire = append(wire, encodeAction(action))
	}
	data, err := json.Marshal(responsesMessage{
		Type:    "responses",
		Seq:     r.st.Seq,
		Seat:    int(seat),
		Options: wire,
	})
	if err != nil {
		r.cfg.logger.Warn("responses 消息序列化失败", "game_id", r.id, "seat", seat, "err", err)
		return
	}
	r.subsMu.Lock()
	targets := make([]*client, 0, len(r.subs))
	for c := range r.subs {
		if c.seat == seat && c.synced {
			targets = append(targets, c)
		}
	}
	r.subsMu.Unlock()
	for _, c := range targets {
		if !c.trySend(data) {
			r.cfg.logger.Warn("响应推送时发送缓冲已满，摘除连接", "game_id", r.id, "seat", c.seat)
			r.dropClient(c)
		}
	}
}

// launchResponseAI 在独立 goroutine 里跑 Agent 的响应决策，带超时；非法/失败回退 pass。
func (r *Room) launchResponseAI(seat game.Seat) {
	obs := r.st.Observe(seat)
	ag := r.cfg.agents[seat]
	go func() {
		started := time.Now()
		ctx, cancel := context.WithTimeout(r.ctx, r.cfg.agentTimeout)
		defer cancel()

		action, err := ag.Decide(ctx, obs)
		elapsed := time.Since(started)
		fallback := err != nil || ctx.Err() != nil || !isResponseAction(obs, action)
		if fallback {
			action = game.Action{Kind: game.ActPass, Seat: seat}
		}
		select {
		case r.aiCh <- aiResult{seat: seat, action: action, elapsed: elapsed, fallback: fallback, response: true}:
		case <-r.quit:
		}
	}()
}

// handleAIResponse 应用一个 AI 响应决策（会再次校验，过期结果直接丢弃）。
func (r *Room) handleAIResponse(res aiResult) {
	r.responseAI[res.seat] = false
	if r.st.Phase != game.PhaseAwaitResponse || r.st.Pending == nil || r.st.Pending.Decided[res.seat] {
		return // 窗口已推进/该座位已响应 → 过期结果
	}
	action := res.action
	action.Seat = res.seat
	obs := r.st.Observe(res.seat)
	if res.fallback || !isResponseAction(obs, action) {
		action = game.Action{Kind: game.ActPass, Seat: res.seat}
		res.fallback = true
	}
	if err := r.applyAction(action); err != nil {
		r.cfg.logger.Warn("AI 响应执行失败，回退 pass",
			"game_id", r.id, "seat", res.seat, "err", err)
		if passErr := r.applyAction(game.Action{Kind: game.ActPass, Seat: res.seat}); passErr != nil {
			return
		}
	}
	r.publishAgentThought(res.seat, action, res.elapsed, obs)
	r.recordDecision(res)
	r.drive()
}

// handleResponseTimeout 处理窗口超时：所有未响应座位按 pass 结算。
func (r *Room) handleResponseTimeout(gen int) {
	if gen != r.responseGen {
		return // 过期定时器（窗口已结束/换新）
	}
	if r.st.Phase != game.PhaseAwaitResponse || r.st.Pending == nil {
		return
	}
	// 注意：不要在这里把 responseActive 置 false —— afterApply 依赖它判断「刚才是否
	// 有窗口在等」，从而广播 response_window_end。若先清零，超时路径前端就永远等不到
	// 窗口结束消息（S11 验收现场 bug）。
	seqBefore := r.st.Seq
	if err := r.st.ResolveTimeout(); err != nil {
		r.cfg.logger.Warn("响应超时结算失败", "game_id", r.id, "err", err)
		return
	}
	r.afterApply(seqBefore, false)
	r.drive()
}

// isResponseAction 校验 action 是否是该座位当前响应窗口的合法选项（chi 还需比对消耗牌）。
func isResponseAction(obs game.Observation, action game.Action) bool {
	for _, legal := range obs.LegalActions {
		if legal.Kind != action.Kind {
			continue
		}
		if legal.Kind == game.ActChi && !sameConsumedKinds(legal.Consumed, action.Consumed) {
			continue
		}
		return true
	}
	return false
}

// sameConsumedKinds 比较两组牌是否包含相同的牌种与张数（吃的 Consumed 校验）。
func sameConsumedKinds(a, b []tile.Tile) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[tile.Kind]int, len(a))
	for _, t := range a {
		counts[t.Kind]++
	}
	for _, t := range b {
		counts[t.Kind]--
		if counts[t.Kind] < 0 {
			return false
		}
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}

// recordDecision 统计一次 AI 决策（耗时 + 超时回退次数）。
func (r *Room) recordDecision(res aiResult) {
	r.metrics.agentDecisions.Add(1)
	if res.fallback {
		r.metrics.agentFallbacks.Add(1)
	}
	r.metrics.latency.observe(res.elapsed)
	if r.cfg.metrics != nil {
		r.cfg.metrics.agentDecisions.Add(1)
		if res.fallback {
			r.cfg.metrics.agentFallbacks.Add(1)
		}
		r.cfg.metrics.latency.observe(res.elapsed)
	}
}

// applyAction 执行一个动作：Apply → 富化事件 → 落库 → 广播 → 刷新快照。
func (r *Room) applyAction(action game.Action) error {
	seqBefore := r.st.Seq
	tsumogiri := false
	if action.Kind == game.ActDiscard && r.st.Drawn != nil && *r.st.Drawn == action.Tile {
		tsumogiri = true
	}
	applyErr := r.st.Apply(action)
	if applyErr != nil {
		// 注意：有些「合法终局」会在返回错误前先写入事件（例如荒牌流局：
		// applyDraw 写入 ryuukyoku/round_end 后返回 error）。这里必须先把新增事件
		// 落库/广播，否则房间会卡在 await_draw 且客户端永远收不到流局事件。
		if r.st.Seq > seqBefore {
			r.afterApply(seqBefore, tsumogiri)
		}
		return applyErr
	}
	r.afterApply(seqBefore, tsumogiri)
	return nil
}

// afterApply 是 Apply 之后的统一收尾：富化新增事件 → 落库 → 广播 → 快照 → 指标，
// 并在离开响应窗口时清理窗口调度状态（停定时器、失效旧代次、清 AI 占位）。
//
// 抽出来是因为「超时 ResolveTimeout」不经过 Apply 但仍会产生事件，需要走同一套收尾。
func (r *Room) afterApply(seqBefore uint64, tsumogiri bool) {
	// 事件已按序追加；len(Events) 与 Seq 同步增长，故 Events[seqBefore:] 即新增事件
	newEvents := r.st.Events[seqBefore:]
	for i := range newEvents {
		enrichEvent(r.st, &newEvents[i], tsumogiri)
	}
	r.persist(newEvents)
	// 先刷新快照再广播：收到事件的客户端立刻 GET 状态时能看到同一 seq，避免竞态
	r.refreshSnapshot()
	r.publish(newEvents)
	r.metrics.actionsApplied.Add(1)
	if r.cfg.metrics != nil {
		r.cfg.metrics.actionsApplied.Add(1)
	}

	if r.st.Phase != game.PhaseAwaitResponse {
		hadWindow := r.responseActive
		r.responseActive = false
		if r.responseTimer != nil {
			r.responseTimer.Stop()
			r.responseTimer = nil
		}
		r.responseGen++ // 让已触发的旧超时信号失效
		for seat := 0; seat < seatCount; seat++ {
			r.responseAI[seat] = false
		}
		if hadWindow {
			// 窗口结束通知（pass/超时/结算都会走到这里），前端据此收起「等待响应」
			r.publishJSON(responseWindowEndMessage{Type: "response_window_end", Seq: r.st.Seq})
		}
	}
}

// publishJSON 把一条非事件消息广播给所有已 resume 的连接（慢连接摘除）。
func (r *Room) publishJSON(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	r.subsMu.Lock()
	targets := make([]*client, 0, len(r.subs))
	for c := range r.subs {
		if c.synced {
			targets = append(targets, c)
		}
	}
	r.subsMu.Unlock()
	for _, c := range targets {
		if !c.trySend(data) {
			r.dropClient(c)
		}
	}
}

// publishAgentThought 广播一条 AI 决策信息（agent_thought）。
func (r *Room) publishAgentThought(seat game.Seat, action game.Action, elapsed time.Duration, obs game.Observation) {
	agentName := r.cfg.agentNames[seat]
	if agentName == "" {
		agentName = "ai"
	}
	chosen := action.Kind.String()
	if action.Kind == game.ActDiscard || action.Kind == game.ActRiichi ||
		action.Kind == game.ActTsumo || action.Kind == game.ActRon {
		if text := action.Tile.String(); text != "" && text != "?" {
			chosen = text
		}
	}
	candidates := make([]string, 0, len(obs.LegalActions))
	for i, legal := range obs.LegalActions {
		if i >= 24 {
			break
		}
		candidates = append(candidates, encodeAction(legal).Kind+" "+
			tileText(legal.Tile))
	}
	r.publishJSON(agentThoughtMessage{
		Type:       "agent_thought",
		Seat:       int(seat),
		Agent:      agentName,
		ElapsedMS:  elapsed.Milliseconds(),
		Chosen:     chosen,
		Candidates: candidates,
	})
}

// tileText 返回动作牌的文本（没有牌时为空串）。
func tileText(t tile.Tile) string {
	if t.String() == "?" {
		return ""
	}
	return t.String()
}

// persist 把新事件写入 store；失败只记日志，不阻塞对局（内存模式不会失败）。
func (r *Room) persist(events []game.Event) {
	if len(events) == 0 {
		return
	}
	if err := r.cfg.store.AppendEvents(r.ctx, r.id, events); err != nil {
		r.cfg.logger.Error("事件落库失败",
			"game_id", r.id, "seq", events[len(events)-1].Seq, "err", err)
		if r.cfg.metrics != nil {
			r.cfg.metrics.errorsTotal.Add(1)
		}
		return
	}
	r.lastSavedSeq = events[len(events)-1].Seq
	r.metrics.eventsPersisted.Add(int64(len(events)))
	if r.cfg.metrics != nil {
		r.cfg.metrics.eventsPersisted.Add(int64(len(events)))
	}
}

// publish 把事件广播给所有已完成 resume 的连接；慢连接直接摘除。
func (r *Room) publish(events []game.Event) {
	if len(events) == 0 {
		return
	}
	r.subsMu.Lock()
	targets := make([]*client, 0, len(r.subs))
	for c := range r.subs {
		if c.synced {
			targets = append(targets, c)
		}
	}
	r.subsMu.Unlock()
	if len(targets) == 0 {
		return
	}

	for _, ev := range events {
		data, err := marshalEvent(ev)
		if err != nil {
			r.cfg.logger.Warn("事件序列化失败", "game_id", r.id, "seq", ev.Seq, "err", err)
			continue
		}
		for _, c := range targets {
			if !c.trySend(data) {
				r.cfg.logger.Warn("WS 客户端发送缓冲已满，摘除连接",
					"game_id", r.id, "seat", c.seat)
				r.dropClient(c)
				continue
			}
			c.lastSentSeq = ev.Seq
		}
	}
}

// dropClient 摘除一个慢/坏连接。
func (r *Room) dropClient(c *client) {
	r.subsMu.Lock()
	delete(r.subs, c)
	r.subsMu.Unlock()
	c.close()
}

// refreshSnapshot 重新发布不可变快照。
func (r *Room) refreshSnapshot() {
	snap := &roomSnapshot{
		seq:           r.st.Seq,
		phase:         r.st.Phase,
		turn:          r.st.Turn,
		wallRemaining: r.st.Wall.Remaining(),
	}
	for seat := game.Seat(0); seat < seatCount; seat++ {
		snap.observations[seat] = r.st.Observe(seat)
	}
	r.snap.Store(snap)
}

// Attach 注册一个 WS 订阅者（不自动回放历史，由客户端 resume 触发补齐）。
func (r *Room) Attach(ctx context.Context, seat game.Seat) (*client, error) {
	return r.attach(ctx, newClient(seat))
}

// AttachSpectator 注册一个只读观战连接（不绑定座位，收全量事件流）。
func (r *Room) AttachSpectator(ctx context.Context) (*client, error) {
	return r.attach(ctx, newSpectatorClient())
}

// attach 把连接句柄交给房间 actor 注册。
func (r *Room) attach(ctx context.Context, c *client) (*client, error) {
	req := &attachRequest{client: c, done: make(chan struct{})}
	select {
	case r.attachCh <- req:
	case <-r.quit:
		return nil, ErrRoomClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-req.done:
	case <-r.quit:
		return nil, ErrRoomClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if r.cfg.metrics != nil {
		r.cfg.metrics.wsConnectionsTotal.Add(1)
		r.cfg.metrics.wsConnectionsActive.Add(1)
	}
	return c, nil
}

// handleAttach 注册订阅者；连接在 resume 前不接收实时广播，避免重复。
func (r *Room) handleAttach(req *attachRequest) {
	r.subsMu.Lock()
	r.subs[req.client] = struct{}{}
	r.subsMu.Unlock()
	close(req.done)
}

// Detach 注销订阅者并关闭连接发送端（WS handler 退出时调用）。
func (r *Room) Detach(c *client) {
	if c == nil {
		return
	}
	r.subsMu.Lock()
	_, existed := r.subs[c]
	delete(r.subs, c)
	r.subsMu.Unlock()
	c.close()
	if existed && r.cfg.metrics != nil {
		r.cfg.metrics.wsConnectionsActive.Add(-1)
	}
}

// Resume 处理断线续传：从 store 补齐 seq > lastSeq 的事件，再回 resumed。
func (r *Room) Resume(ctx context.Context, c *client, lastSeq uint64) error {
	req := &resumeRequest{client: c, lastSeq: lastSeq, reply: make(chan error, 1)}
	select {
	case r.resumeCh <- req:
	case <-r.quit:
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-r.quit:
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleResume 在 actor 内执行补齐：与广播串行，因此不会与实时事件交错误序。
func (r *Room) handleResume(req *resumeRequest) {
	events, err := r.cfg.store.LoadEvents(r.ctx, r.id, req.lastSeq)
	if err != nil {
		req.reply <- fmt.Errorf("load events: %w", err)
		return
	}
	for _, ev := range events {
		if ev.Seq <= req.client.lastSentSeq {
			continue // 该连接已经发过，避免重复
		}
		data, err := marshalEvent(ev)
		if err != nil {
			req.reply <- fmt.Errorf("marshal event: %w", err)
			return
		}
		if !req.client.trySend(data) {
			r.cfg.logger.Warn("WS 续传时发送缓冲已满，摘除连接",
				"game_id", r.id, "seat", req.client.seat)
			r.dropClient(req.client)
			req.reply <- ErrRoomClosed
			return
		}
		req.client.lastSentSeq = ev.Seq
	}

	current := r.st.Seq
	if req.client.lastSentSeq < current {
		req.client.lastSentSeq = current
	}
	from := req.lastSeq + 1
	if from > current {
		from = current
	}
	req.client.synced = true
	if r.cfg.metrics != nil {
		r.cfg.metrics.wsResumes.Add(1)
	}
	req.client.sendMessage(resumedMessage{Type: "resumed", FromSeq: from, ToSeq: current})

	// 断线重连时如果正处在响应窗口且该座位还没响应，补推一次 responses，
	// 否则玩家会一直等到超时。观战连接不接收响应选项。
	if !req.client.spectator && r.st.Phase == game.PhaseAwaitResponse && r.st.Pending != nil && !r.st.Pending.Decided[req.client.seat] {
		r.pushResponsesTo(req.client)
	}
	req.reply <- nil
}

// pushResponsesTo 把某座位的响应选项推给单条连接（resume 补推用）。
func (r *Room) pushResponsesTo(c *client) {
	options := r.st.Responses(c.seat)
	if len(options) == 0 {
		return
	}
	wire := make([]actionWire, 0, len(options))
	for _, action := range options {
		wire = append(wire, encodeAction(action))
	}
	c.sendMessage(responsesMessage{
		Type:    "responses",
		Seq:     r.st.Seq,
		Seat:    int(c.seat),
		Options: wire,
	})
}

// SubmitAction 提交一个客户端动作（人类座位）。
func (r *Room) SubmitAction(ctx context.Context, seat game.Seat, action game.Action, actionID string, seq uint64) error {
	req := &actionRequest{seat: seat, action: action, actionID: actionID, seq: seq, reply: make(chan error, 1)}
	select {
	case r.actionCh <- req:
	case <-r.quit:
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-r.quit:
		return ErrRoomClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleAction 校验并执行客户端动作；同一 action_id 重复提交只执行一次。
func (r *Room) handleAction(req *actionRequest) {
	if req.actionID != "" {
		if _, ok := r.processedActionIDs[req.actionID]; ok {
			req.reply <- nil // 幂等：重复提交视为成功，不重复 Apply
			return
		}
	}
	if req.seq != 0 && req.seq != r.st.Seq {
		req.reply <- fmt.Errorf("%w: 客户端 seq=%d，当前 seq=%d，请先 resume",
			ErrStaleSeq, req.seq, r.st.Seq)
		return
	}
	if r.cfg.agents[req.seat] != nil {
		req.reply <- fmt.Errorf("%w: 座位 %d 由 AI 控制，不接受客户端动作",
			game.ErrIllegalAction, req.seat)
		return
	}
	// 动作的座位以「连接身份」为准，不信任消息体里的座位字段（防越权）
	action := req.action
	action.Seat = req.seat

	// 响应窗口：只接受该窗口内该座位的响应动作（ron/pon/chi/kan/pass），
	// 合法性由 game.Apply 统一校验（含 ron 的振听、吃的 consumed 组合等）。
	if r.st.Phase == game.PhaseAwaitResponse {
		if r.st.Pending == nil || r.st.Pending.Decided[req.seat] {
			req.reply <- fmt.Errorf("%w: seat %d 当前不在待响应集合（可能已响应或窗口已结束）",
				game.ErrIllegalAction, req.seat)
			return
		}
	}

	if err := r.applyAction(action); err != nil {
		req.reply <- err
		return
	}
	r.rememberActionID(req.actionID)
	r.drive()
	req.reply <- nil
}

// rememberActionID 记录已执行的 action_id（有界去重表，超出上限淘汰最旧）。
func (r *Room) rememberActionID(actionID string) {
	if actionID == "" {
		return
	}
	r.processedActionIDs[actionID] = struct{}{}
	r.actionIDOrder = append(r.actionIDOrder, actionID)
	if len(r.actionIDOrder) > actionIDCacheLimit {
		oldest := r.actionIDOrder[0]
		r.actionIDOrder = r.actionIDOrder[1:]
		delete(r.processedActionIDs, oldest)
	}
}

// Metrics 返回单局指标快照。
func (r *Room) Metrics() roomMetricsSnapshot {
	out := roomMetricsSnapshot{
		GameID:          r.id,
		EventsPersisted: r.metrics.eventsPersisted.Load(),
		ActionsApplied:  r.metrics.actionsApplied.Load(),
		AgentDecisions:  r.metrics.agentDecisions.Load(),
		AgentFallbacks:  r.metrics.agentFallbacks.Load(),
		DecisionLatency: r.metrics.latency.snapshot(),
	}
	if snap := r.snap.Load(); snap != nil {
		out.Seq = snap.seq
		out.Phase = phaseName(snap.phase)
		out.WallRemaining = snap.wallRemaining
	}
	return out
}
