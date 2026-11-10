package transport

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

// 支持的 Agent 名称（建局请求 agents 字段的取值）。
const (
	AgentHuman      = "human"       // 人类座位：等 WS action，服务端不自动决策
	AgentRandom     = "random"      // 随机策略（agent.Random）
	AgentFirstLegal = "first_legal" // 首个合法动作（agent.FirstLegal）
)

// DefaultAgents 是请求未指定 agents 时的默认配置：1 人类 + 3 AI（产品目标形态）。
var DefaultAgents = []string{AgentHuman, AgentRandom, AgentRandom, AgentRandom}

// ErrInvalidRequest 表示请求参数非法（HTTP 400 / WS VALIDATION）。
var ErrInvalidRequest = errors.New("transport: invalid request")

// CreateSpec 是建局参数。
type CreateSpec struct {
	Seed    int64
	Ruleset game.Ruleset
	Agents  []string
}

// ManagerConfig 是房间管理器配置。
type ManagerConfig struct {
	Store        store.Store
	AgentTimeout time.Duration
	// AutoPassResponses=true 表示兼容模式：响应窗口全部自动 pass（S6 行为）；
	// false（默认）表示真打模式：打牌/加杠后把响应选项推给人类座位、AI 自动决策。
	AutoPassResponses bool
	// ResponseTimeout 是人类响应窗口超时（超时按 pass 处理，0 表示默认 5s）。
	ResponseTimeout time.Duration
	Logger          *slog.Logger
	Metrics         *metrics
}

// Manager 管理所有对局房间（单进程内存）。
type Manager struct {
	mu      sync.RWMutex
	rooms   map[string]*Room
	store   store.Store
	cfg     ManagerConfig
	metrics *metrics
	logger  *slog.Logger
	closed  atomic.Bool
}

// NewManager 构造房间管理器；Store 为空时使用内存实现。
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = newMetrics()
	}
	return &Manager{
		rooms:   make(map[string]*Room),
		store:   newSyncStore(cfg.Store),
		cfg:     cfg,
		metrics: cfg.Metrics,
		logger:  cfg.Logger,
	}
}

// Store 返回管理器使用的（并发安全的）事件存储。
func (m *Manager) Store() store.Store { return m.store }

// Create 建局：校验参数 → 发牌 → 落库元数据与初始事件 → 注册房间并启动 actor。
func (m *Manager) Create(ctx context.Context, spec CreateSpec) (*Room, error) {
	if m.closed.Load() {
		return nil, ErrRoomClosed
	}
	agents, names, err := resolveAgents(spec.Agents, spec.Seed)
	if err != nil {
		return nil, err
	}
	rules := spec.Ruleset
	if rules.StartingPoints <= 0 {
		rules.StartingPoints = game.DefaultRuleset().StartingPoints
	}
	if rules.RedFives < 0 || rules.RedFives > 3 {
		return nil, fmt.Errorf("%w: ruleset.red_fives 必须在 0..3", ErrInvalidRequest)
	}

	st, err := game.NewWithOptions(spec.Seed, rules, game.Options{
		// 真打模式：显式响应窗口；兼容模式则沿用「自动 pass」。
		ManualResponses: !m.cfg.AutoPassResponses,
		// 真实算点（rules.Evaluate/Score），S5b task-14 起为默认。
		ScoreProvider: game.RulesScorer{},
	})
	if err != nil {
		return nil, fmt.Errorf("发起对局: %w", err)
	}
	id, err := newGameID()
	if err != nil {
		return nil, fmt.Errorf("生成对局 id: %w", err)
	}
	meta := store.GameMeta{
		ID:        id,
		Seed:      spec.Seed,
		Rules:     rules,
		Agents:    append([]string(nil), names[:]...),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.CreateGame(ctx, meta); err != nil {
		return nil, fmt.Errorf("写入对局元数据: %w", err)
	}

	roomCfg := roomConfig{
		store:           m.store,
		agents:          agents,
		agentNames:      names,
		agentTimeout:    m.cfg.AgentTimeout,
		manualResponses: !m.cfg.AutoPassResponses,
		responseTimeout: m.cfg.ResponseTimeout,
		logger:          m.logger,
		metrics:         m.metrics,
	}
	room := newRoom(id, st, meta, roomCfg)
	if err := room.persistInitial(ctx); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.rooms[id] = room
	m.mu.Unlock()
	m.metrics.gamesCreated.Add(1)
	m.metrics.gamesActive.Add(1)
	m.logger.Info("对局创建", "game_id", id, "seed", spec.Seed, "agents", names)
	room.Start()
	return room, nil
}

// Get 按 id 取房间。
func (m *Manager) Get(id string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	room, ok := m.rooms[id]
	return room, ok
}

// ActiveCount 返回当前保留的对局数。
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

// Close 关闭所有房间（进程退出时调用）。
func (m *Manager) Close() {
	if !m.closed.CompareAndSwap(false, true) {
		return
	}
	m.mu.Lock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, room := range m.rooms {
		rooms = append(rooms, room)
	}
	m.rooms = make(map[string]*Room)
	m.mu.Unlock()

	for _, room := range rooms {
		room.Close()
		m.metrics.gamesActive.Add(-1)
		m.metrics.gamesClosed.Add(1)
	}
}

// resolveAgents 把策略名解析为 Agent 实例；nil 表示人类座位。
func resolveAgents(names []string, seed int64) ([seatCount]agent.Agent, [seatCount]string, error) {
	var agents [seatCount]agent.Agent
	var resolved [seatCount]string

	list := names
	if len(list) == 0 {
		list = DefaultAgents
	}
	if len(list) != seatCount {
		return agents, resolved, fmt.Errorf("%w: agents 必须恰好 4 个（当前 %d 个）",
			ErrInvalidRequest, len(list))
	}
	for i, name := range list {
		switch name {
		case "", AgentHuman:
			resolved[i] = AgentHuman // human 座位不创建 Agent
		case AgentRandom:
			// 每个座位独立随机源，seed + 座位号保证可复现且不同座位不同序列
			agents[i] = agent.NewRandom(seed + int64(i)*1000003)
			resolved[i] = AgentRandom
		case AgentFirstLegal:
			agents[i] = agent.FirstLegal{}
			resolved[i] = AgentFirstLegal
		default:
			return agents, resolved, fmt.Errorf("%w: 未知 agent %q（支持 human/random/first_legal）",
				ErrInvalidRequest, name)
		}
	}
	return agents, resolved, nil
}

// newGameID 生成 32 位十六进制对局 id（16 字节随机）。
func newGameID() (string, error) {
	var buf [16]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// randomSeed 生成随机种子（请求未指定 seed 时使用）。
func randomSeed() int64 {
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return time.Now().UnixNano()
	}
	return int64(binary.LittleEndian.Uint64(buf[:]))
}
