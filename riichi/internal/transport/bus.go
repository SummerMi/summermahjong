package transport

import (
	"context"
	"sync"

	"github.com/redis/go-redis/v9"
)

// Bus 是跨实例的房间事件广播通道：
//   - 房间把事件帧（与 WS 相同的 JSON）发布到 `riichi:room:<id>`；
//   - 其它实例的观战镜像订阅该 channel，把帧转发给本地观战连接。
type Bus interface {
	Publish(ctx context.Context, channel string, payload []byte) error
	Subscribe(ctx context.Context, channel string) (<-chan []byte, func(), error)
}

// roomChannel 返回某对局的广播频道名。
func roomChannel(gameID string) string { return "riichi:room:" + gameID }

// MemoryBus 是进程内实现（单实例 / 测试用；不跨进程）。
type MemoryBus struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]struct{}
}

// NewMemoryBus 构造进程内总线。
func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: make(map[string]map[chan []byte]struct{})}
}

// Publish 实现 Bus：非阻塞投递给所有订阅者（慢订阅者丢帧）。
func (b *MemoryBus) Publish(_ context.Context, channel string, payload []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[channel] {
		select {
		case ch <- payload:
		default:
		}
	}
	return nil
}

// Subscribe 实现 Bus：返回帧 channel 与取消函数。
//
// 取消只做「注销」，不关闭 channel（可能仍有 Publish 在读锁下投递）；
// 调用方的读取 goroutine 由 cancel 自行退出。
func (b *MemoryBus) Subscribe(_ context.Context, channel string) (<-chan []byte, func(), error) {
	ch := make(chan []byte, 256)
	b.mu.Lock()
	if b.subs[channel] == nil {
		b.subs[channel] = make(map[chan []byte]struct{})
	}
	b.subs[channel][ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		delete(b.subs[channel], ch)
		if len(b.subs[channel]) == 0 {
			delete(b.subs, channel)
		}
		b.mu.Unlock()
	}
	return ch, cancel, nil
}

// RedisBus 是基于 Redis pub/sub 的跨进程实现。
type RedisBus struct {
	client *redis.Client
}

// NewRedisBus 构造 Redis 总线。
func NewRedisBus(client *redis.Client) *RedisBus {
	return &RedisBus{client: client}
}

// Publish 实现 Bus。
func (b *RedisBus) Publish(ctx context.Context, channel string, payload []byte) error {
	return b.client.Publish(ctx, channel, payload).Err()
}

// Subscribe 实现 Bus：先等待订阅确认，再交给读取 goroutine。
func (b *RedisBus) Subscribe(ctx context.Context, channel string) (<-chan []byte, func(), error) {
	sub := b.client.Subscribe(ctx, channel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, err
	}
	out := make(chan []byte, 256)
	done := make(chan struct{})
	go func() {
		defer close(out)
		messages := sub.Channel()
		for {
			select {
			case <-done:
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-done:
					return
				}
			}
		}
	}()
	cancel := func() {
		close(done)
		_ = sub.Close()
	}
	return out, cancel, nil
}
