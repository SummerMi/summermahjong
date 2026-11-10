package transport

import (
	"context"
	"sync"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

// syncStore 给 store.Store 套一层互斥锁，让「内存实现 + 多房间并发」安全。
//
// 为什么需要：store.Memory 用普通 map 实现（见 internal/store/store.go），
// 多 goroutine 并发 CreateGame/AppendEvents 会触发 "concurrent map writes" panic。
// 200 并发建局是本切片验收项，所以 transport 层统一包一层锁；SQL/Redis 实现本身
// 线程安全，这层锁只是额外一次无争用开销，换来「换存储实现不改调用方」的一致性。
//
// 注意：锁的粒度是「整个 Store」，不同对局的事件追加会被串行化。对单机 demo 与
// 200 并发建局足够；生产可以把锁换成 per-game 锁或直接在 SQL 事务里解决。
type syncStore struct {
	mu    sync.Mutex
	inner store.Store
}

// newSyncStore 包装一个 Store；nil 时使用内存实现。
func newSyncStore(inner store.Store) store.Store {
	if inner == nil {
		return &syncStore{inner: store.NewMemory()}
	}
	return &syncStore{inner: inner}
}

// CreateGame 实现 store.Store。
func (s *syncStore) CreateGame(ctx context.Context, meta store.GameMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.CreateGame(ctx, meta)
}

// GetMeta 实现 store.Store。
func (s *syncStore) GetMeta(ctx context.Context, gameID string) (store.GameMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.GetMeta(ctx, gameID)
}

// AppendEvents 实现 store.Store。
func (s *syncStore) AppendEvents(ctx context.Context, gameID string, events []game.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.AppendEvents(ctx, gameID, events)
}

// LoadEvents 实现 store.Store。
func (s *syncStore) LoadEvents(ctx context.Context, gameID string, fromSeq uint64) ([]game.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.LoadEvents(ctx, gameID, fromSeq)
}

// SaveSnapshot 实现 store.Store。
func (s *syncStore) SaveSnapshot(ctx context.Context, gameID string, atSeq uint64, snapshot []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.SaveSnapshot(ctx, gameID, atSeq, snapshot)
}

// LoadSnapshot 实现 store.Store。
func (s *syncStore) LoadSnapshot(ctx context.Context, gameID string) ([]byte, uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.LoadSnapshot(ctx, gameID)
}
