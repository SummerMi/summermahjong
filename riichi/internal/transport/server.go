package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

const (
	// defaultAgentDecisionTimeout 是 AI 决策默认上限（契约 §4：500ms）。
	defaultAgentDecisionTimeout = 500 * time.Millisecond
	wsWriteTimeout              = 10 * time.Second
	wsPingPeriod                = 30 * time.Second
	wsIdleTimeout               = 120 * time.Second
	wsReadLimit                 = 1 << 20
	maxRequestBody              = 1 << 20
)

// Config 是 Server 配置。
type Config struct {
	// JWTSecret 是 JWT 签名密钥（必填；生产从环境变量注入）。
	JWTSecret []byte
	// AgentTimeout 是 AI 单次决策上限，0 表示默认 500ms。
	AgentTimeout time.Duration
	// AutoPassResponses=true 时响应窗口全部自动 pass（S6 兼容模式）；
	// 默认 false = 真打模式：打牌/加杠后把响应选项推给人类座位。
	AutoPassResponses bool
	// ResponseTimeout 是人类响应窗口超时，0 表示默认 5s（超时按 pass 结算）。
	ResponseTimeout time.Duration
	// AutoAdvanceRounds 控制本局结束后是否自动开下一局（整场连打）。
	// nil = 默认开启（demo/生产连打）；显式 false = 停在 round_end（单局调试/旧测试）。
	AutoAdvanceRounds *bool
	// Store 是事件存储，nil 表示使用内存实现（测试 / 单机 demo）。
	Store store.Store
	// Logger 是结构化日志，nil 表示 slog.Default()。
	Logger *slog.Logger

	// CORSOrigin 是允许的跨域来源；空串表示不启用（demo 传 "*"，生产传白名单域名）。
	CORSOrigin string
}

// Server 是对局服务：HTTP + WebSocket。
type Server struct {
	cfg      Config
	logger   *slog.Logger
	store    store.Store
	manager  *Manager
	metrics  *metrics
	upgrader websocket.Upgrader
}

// New 构造对局服务。
func New(cfg Config) (*Server, error) {
	if len(cfg.JWTSecret) == 0 {
		return nil, errors.New("transport: JWTSecret 不能为空（请通过 RIICHI_JWT_SECRET 注入）")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.AgentTimeout <= 0 {
		cfg.AgentTimeout = defaultAgentDecisionTimeout
	}
	m := newMetrics()
	manager := NewManager(ManagerConfig{
		Store:             cfg.Store,
		AgentTimeout:      cfg.AgentTimeout,
		AutoPassResponses: cfg.AutoPassResponses,
		ResponseTimeout:   cfg.ResponseTimeout,
		AutoAdvanceRounds: cfg.AutoAdvanceRounds,
		Logger:            cfg.Logger,
		Metrics:           m,
	})
	return &Server{
		cfg:     cfg,
		logger:  cfg.Logger,
		store:   manager.Store(),
		manager: manager,
		metrics: m,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// demo 允许所有来源；生产要按 Origin 白名单校验
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}, nil
}

// Handler 返回带路由与 panic 兜底的 HTTP handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/games", s.handleCreateGame)
	mux.HandleFunc("GET /api/v1/games/{id}", s.handleGetGame)
	mux.HandleFunc("GET /api/v1/games/{id}/replay", s.handleReplay)
	mux.HandleFunc("GET /api/v1/games/{id}/metrics", s.handleGameMetrics)
	mux.HandleFunc("GET /api/v1/ws/games/{id}", s.handleWS)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return withCORS(s.recoverPanics(mux), s.cfg.CORSOrigin)
}

// Close 关闭所有房间（进程退出时调用）。
func (s *Server) Close() { s.manager.Close() }

// Manager 暴露房间管理器（测试 / 运维用）。
func (s *Server) Manager() *Manager { return s.manager }

// recoverPanics 兜底 panic → 500 INTERNAL，避免单个请求打挂进程。
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("请求 panic", "method", r.Method, "path", r.URL.Path, "panic", rec)
				s.metrics.errorsTotal.Add(1)
				writeAPIError(w, http.StatusInternalServerError, CodeInternal, "内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ── HTTP handlers ───────────────────────────────────────────────

// createGameRequest 是 POST /api/v1/games 的请求体。
//
// 契约 §8 的 kuitan/double_ron 等字段 M2 才消费，这里不声明也不报错
// （encoding/json 默认忽略未知字段，符合「允许新增字段」的兼容规则）。
type createGameRequest struct {
	Seed    *int64          `json:"seed"`
	Ruleset *rulesetRequest `json:"ruleset"`
	Agents  []string        `json:"agents"`
}

// rulesetRequest 是建局请求里的规则子集（M1 只有赤宝牌数与起始点数）。
type rulesetRequest struct {
	RedFives       *int `json:"red_fives"`
	StartingPoints *int `json:"starting_points"`
}

// toSpec 把请求体转成建局参数（补默认值并做范围校验）。
func (req *createGameRequest) toSpec() (CreateSpec, error) {
	spec := CreateSpec{
		Seed:    randomSeed(),
		Ruleset: game.DefaultRuleset(),
		Agents:  req.Agents,
	}
	if req.Seed != nil {
		spec.Seed = *req.Seed
	}
	if req.Ruleset != nil {
		if req.Ruleset.RedFives != nil {
			spec.Ruleset.RedFives = *req.Ruleset.RedFives
		}
		if req.Ruleset.StartingPoints != nil {
			spec.Ruleset.StartingPoints = *req.Ruleset.StartingPoints
		}
	}
	if spec.Ruleset.RedFives < 0 || spec.Ruleset.RedFives > 3 {
		return spec, fmt.Errorf("%w: ruleset.red_fives 必须在 0..3", ErrInvalidRequest)
	}
	if spec.Ruleset.StartingPoints <= 0 {
		spec.Ruleset.StartingPoints = game.DefaultRuleset().StartingPoints
	}
	return spec, nil
}

// handleCreateGame 处理建局。需要 JWT（任意座位令牌）。
func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		writeError(w, err)
		return
	}

	var req createGameRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, CodeValidation, "请求体不是合法 JSON："+err.Error())
		return
	}
	spec, err := req.toSpec()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	room, err := s.manager.Create(r.Context(), spec)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			writeAPIError(w, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
		s.metrics.errorsTotal.Add(1)
		writeError(w, err)
		return
	}

	snap := room.Snapshot()
	resp := createGameResponse{
		GameID: room.ID(),
		Seed:   room.Meta().Seed,
		Agents: room.Agents(),
		Ruleset: rulesetWire{
			RedFives:       room.Meta().Rules.RedFives,
			StartingPoints: room.Meta().Rules.StartingPoints,
		},
		CreatedAt: room.Meta().CreatedAt,
	}
	if snap != nil {
		resp.Seq = snap.seq
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleGetGame 返回按令牌座位裁剪的当前状态（game.Observe）。
func (s *Server) handleGetGame(w http.ResponseWriter, r *http.Request) {
	seat, err := s.authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}
	room, ok := s.manager.Get(r.PathValue("id"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, CodeGameNotFound, "对局不存在："+r.PathValue("id"))
		return
	}
	snap := room.Snapshot()
	if snap == nil {
		writeAPIError(w, http.StatusInternalServerError, CodeInternal, "对局状态尚未就绪")
		return
	}
	writeJSON(w, http.StatusOK,
		toObserveResponse(room.ID(), snap.seq, snap.phase, snap.turn, snap.observations[seat]))
}

// handleReplay 返回全量事件（回放用）。
func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		writeError(w, err)
		return
	}
	gameID := r.PathValue("id")
	if _, ok := s.manager.Get(gameID); !ok {
		writeAPIError(w, http.StatusNotFound, CodeGameNotFound, "对局不存在："+gameID)
		return
	}
	events, err := s.store.LoadEvents(r.Context(), gameID, 0)
	if err != nil {
		if errors.Is(err, store.ErrGameNotFound) {
			writeAPIError(w, http.StatusNotFound, CodeGameNotFound, "对局不存在："+gameID)
			return
		}
		writeError(w, err)
		return
	}
	resp := replayResponse{GameID: gameID, Events: make([]eventMessage, 0, len(events))}
	for _, ev := range events {
		resp.Events = append(resp.Events, newEventMessage(ev))
	}
	if len(events) > 0 {
		resp.ToSeq = events[len(events)-1].Seq
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGameMetrics 返回单局指标（契约 §6.1）。
func (s *Server) handleGameMetrics(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		writeError(w, err)
		return
	}
	room, ok := s.manager.Get(r.PathValue("id"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, CodeGameNotFound, "对局不存在："+r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, room.Metrics())
}

// handleMetrics 返回进程级指标。
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.metrics.snapshot())
}

// handleHealth 健康检查。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// ── 鉴权 ────────────────────────────────────────────────────────

// authenticate 从 Authorization: Bearer 或 ?token= 解析令牌，返回座位。
func (s *Server) authenticate(r *http.Request) (game.Seat, error) {
	token := tokenFromRequest(r.Header.Get("Authorization"), r.URL.Query().Get("token"))
	if token == "" {
		return 0, ErrTokenMissing
	}
	return ParseToken(s.cfg.JWTSecret, token)
}

// ── WebSocket ──────────────────────────────────────────────────

// handleWS 处理 /api/v1/ws/games/{id}?seat=&token=。
//
// 握手阶段校验顺序：seat 参数（400）→ 令牌（401）→ 座位绑定（403）→ 对局存在（404），
// 与契约 §7 的错误码一致；握手失败用 HTTP 状态码返回 JSON 信封，不升级协议。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	gameID := r.PathValue("id")

	seatText := r.URL.Query().Get("seat")
	seat, err := parseSeat(seatText)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, CodeValidation,
			fmt.Sprintf("seat 必须是 0..3 的整数，收到 %q", seatText))
		return
	}
	tokenSeat, err := s.authenticate(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if tokenSeat != seat {
		writeError(w, wrapSeatMismatch(tokenSeat, seat))
		return
	}
	room, ok := s.manager.Get(gameID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, CodeGameNotFound, "对局不存在："+gameID)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("WS 升级失败", "game_id", gameID, "seat", seat, "err", err)
		return
	}
	client, err := room.Attach(r.Context(), seat)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() {
		room.Detach(client)
		_ = conn.Close()
	}()

	s.logger.Info("WS 连接建立", "game_id", gameID, "seat", seat)
	go s.writePump(conn, client)
	s.readPump(conn, room, client)
	s.logger.Info("WS 连接关闭", "game_id", gameID, "seat", seat)
}

// parseSeat 解析座位号。
func parseSeat(text string) (game.Seat, error) {
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%w: seat %q", ErrInvalidRequest, text)
	}
	if value < 0 || value >= seatCount {
		return 0, fmt.Errorf("%w: seat %d", ErrInvalidRequest, value)
	}
	return game.Seat(value), nil
}

// readPump 读取客户端消息直到连接关闭。
func (s *Server) readPump(conn *websocket.Conn, room *Room, c *client) {
	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
	})
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
		s.handleClientMessage(room, c, data)
	}
}

// writePump 串行写消息并定时发 ping，检测假死连接。
func (s *Server) writePump(conn *websocket.Conn, c *client) {
	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server closed"))
			_ = conn.Close()
			return
		case message := <-c.send:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				_ = conn.Close()
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

// handleClientMessage 分发一条客户端协议消息。
func (s *Server) handleClientMessage(room *Room, c *client, data []byte) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		c.sendError(CodeValidation, "消息必须是合法 JSON")
		return
	}

	switch env.Type {
	case "ping":
		var msg pingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.sendError(CodeValidation, "ping 消息格式错误")
			return
		}
		c.sendMessage(pongMessage{Type: "pong", Ts: msg.Ts})

	case "resume":
		var msg resumeMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.sendError(CodeValidation, "resume 消息格式错误")
			return
		}
		if err := room.Resume(context.Background(), c, msg.LastSeq); err != nil {
			c.sendError(errorCodeFor(err), err.Error())
		}

	case "action":
		var msg actionMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.sendError(CodeValidation, "action 消息格式错误")
			return
		}
		action, err := parseAction(msg.Action)
		if err != nil {
			c.sendError(errorCodeFor(err), err.Error())
			return
		}
		if err := room.SubmitAction(context.Background(), c.seat, action, msg.ActionID, msg.Seq); err != nil {
			c.sendError(errorCodeFor(err), err.Error())
		}

	default:
		c.sendError(CodeValidation, fmt.Sprintf("未知消息类型 %q", env.Type))
	}
}
