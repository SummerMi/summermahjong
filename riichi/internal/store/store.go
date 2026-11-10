// Package store 定义对局事件与快照的持久化接口。
//
// 契约（docs/riichi-接口契约-20261110.md §5）：
//   - 三种实现（memory / sql / redis）行为必须一致；
//   - 事件按 (game_id, seq) 单调追加，LoadEvents(fromSeq) 返回 > fromSeq 的事件；
//   - 快照与全量回放必须等价（由测试保证）。
package store

import (
	"context"
	"errors"
	"time"

	"summermahjong/riichi/internal/game"
)

// ErrGameNotFound 表示对局不存在。
var ErrGameNotFound = errors.New("store: game not found")

// GameMeta 是一局对局的元数据。
type GameMeta struct {
	ID        string
	Seed      int64
	Rules     game.Ruleset
	Agents    []string
	CreatedAt time.Time
}

// Store 是持久化接口。
type Store interface {
	// CreateGame 写入元数据；已存在返回 nil（幂等）。
	CreateGame(ctx context.Context, meta GameMeta) error
	// AppendEvents 追加事件；seq 必须单调递增。
	AppendEvents(ctx context.Context, gameID string, events []game.Event) error
	// LoadEvents 返回 seq > fromSeq 的事件（升序）。
	LoadEvents(ctx context.Context, gameID string, fromSeq uint64) ([]game.Event, error)
	// SaveSnapshot 保存某 seq 处的状态快照（调用方自行序列化）。
	SaveSnapshot(ctx context.Context, gameID string, atSeq uint64, snapshot []byte) error
	// LoadSnapshot 返回最新快照；没有快照时 ok=false。
	LoadSnapshot(ctx context.Context, gameID string) (snapshot []byte, atSeq uint64, ok bool, err error)
	// GetMeta 读取元数据。
	GetMeta(ctx context.Context, gameID string) (GameMeta, error)
}

// Memory 是内存实现（默认，用于测试与单机 demo）。
type Memory struct {
	metas     map[string]GameMeta
	events    map[string][]game.Event
	snapshots map[string][]byte
	snapSeq   map[string]uint64
}

// NewMemory 构造内存存储。
func NewMemory() *Memory {
	return &Memory{
		metas:     make(map[string]GameMeta),
		events:    make(map[string][]game.Event),
		snapshots: make(map[string][]byte),
		snapSeq:   make(map[string]uint64),
	}
}

// CreateGame 实现 Store。
func (m *Memory) CreateGame(_ context.Context, meta GameMeta) error {
	if _, ok := m.metas[meta.ID]; !ok {
		m.metas[meta.ID] = meta
	}
	return nil
}

// AppendEvents 实现 Store。
func (m *Memory) AppendEvents(_ context.Context, gameID string, events []game.Event) error {
	if _, ok := m.metas[gameID]; !ok {
		return ErrGameNotFound
	}
	m.events[gameID] = append(m.events[gameID], events...)
	return nil
}

// LoadEvents 实现 Store。
func (m *Memory) LoadEvents(_ context.Context, gameID string, fromSeq uint64) ([]game.Event, error) {
	if _, ok := m.metas[gameID]; !ok {
		return nil, ErrGameNotFound
	}
	all := m.events[gameID]
	out := make([]game.Event, 0, len(all))
	for _, e := range all {
		if e.Seq > fromSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

// SaveSnapshot 实现 Store。
func (m *Memory) SaveSnapshot(_ context.Context, gameID string, atSeq uint64, snapshot []byte) error {
	if _, ok := m.metas[gameID]; !ok {
		return ErrGameNotFound
	}
	m.snapshots[gameID] = snapshot
	m.snapSeq[gameID] = atSeq
	return nil
}

// LoadSnapshot 实现 Store。
func (m *Memory) LoadSnapshot(_ context.Context, gameID string) ([]byte, uint64, bool, error) {
	if _, ok := m.metas[gameID]; !ok {
		return nil, 0, false, ErrGameNotFound
	}
	snap, ok := m.snapshots[gameID]
	return snap, m.snapSeq[gameID], ok, nil
}

// GetMeta 实现 Store。
func (m *Memory) GetMeta(_ context.Context, gameID string) (GameMeta, error) {
	meta, ok := m.metas[gameID]
	if !ok {
		return GameMeta{}, ErrGameNotFound
	}
	return meta, nil
}
