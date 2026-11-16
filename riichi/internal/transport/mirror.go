package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

// ErrReadOnly 表示观战连接不能提交动作。
var ErrReadOnly = errors.New("transport: 观战连接只读")

// mirror 是非本实例托管的房间的只读镜像：
// 订阅 Redis/Memory bus 的频道，转发实时帧，并在 resume 时从共享 store 回放历史。
type mirror struct {
	id      string
	store   store.Store
	channel string
	ctx     context.Context
	cancel  func()

	mu   sync.Mutex
	subs map[*client]struct{}
}

// newMirror 创建镜像并启动订阅 goroutine。
func newMirror(ctx context.Context, id string, st store.Store, bus Bus) (*mirror, error) {
	channel := roomChannel(id)
	frames, cancel, err := bus.Subscribe(ctx, channel)
	if err != nil {
		return nil, err
	}
	m := &mirror{
		id:      id,
		store:   st,
		channel: channel,
		ctx:     ctx,
		cancel:  cancel,
		subs:    make(map[*client]struct{}),
	}
	go m.run(frames)
	return m, nil
}

// add 注册一个本地观战连接。
func (m *mirror) add(c *client) {
	m.mu.Lock()
	m.subs[c] = struct{}{}
	m.mu.Unlock()
}

// Detach 注销观战连接。
func (m *mirror) Detach(c *client) {
	if c == nil {
		return
	}
	m.mu.Lock()
	delete(m.subs, c)
	m.mu.Unlock()
	c.close()
}

// Resume 从共享 store 补齐 seq > lastSeq 的事件，然后进入实时转发。
//
// 与 run 共用 m.mu：resume 期间收到的实时帧会先在 run 里等锁，
// 等 resume 把 synced 置位后再转发，避免重复/乱序。
func (m *mirror) Resume(ctx context.Context, c *client, lastSeq uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	events, err := m.store.LoadEvents(ctx, m.id, lastSeq)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if ev.Seq <= c.lastSentSeq {
			continue
		}
		data, err := marshalEvent(ev)
		if err != nil {
			return err
		}
		if !c.trySend(data) {
			return ErrRoomClosed
		}
		c.lastSentSeq = ev.Seq
	}
	to := c.lastSentSeq
	from := lastSeq + 1
	if from > to {
		from = to
	}
	c.synced = true
	c.sendMessage(resumedMessage{Type: "resumed", FromSeq: from, ToSeq: to})
	return nil
}

// SubmitAction 对镜像连接永远拒绝（写路径只允许在房间 owner 上执行）。
func (m *mirror) SubmitAction(context.Context, game.Seat, game.Action, string, uint64) error {
	return ErrReadOnly
}

// run 把 bus 帧转发给本地已同步的观战连接（按 seq 去重）。
func (m *mirror) run(frames <-chan []byte) {
	for frame := range frames {
		seq, isEvent := frameEventSeq(frame)
		m.mu.Lock()
		for c := range m.subs {
			if !c.synced {
				continue
			}
			if isEvent && seq <= c.lastSentSeq {
				continue
			}
			if !c.trySend(frame) {
				delete(m.subs, c)
				c.close()
				continue
			}
			if isEvent {
				c.lastSentSeq = seq
			}
		}
		m.mu.Unlock()
	}
}

// frameEventSeq 从帧里读 type/seq；非事件消息返回 isEvent=false。
func frameEventSeq(frame []byte) (uint64, bool) {
	var env struct {
		Type string `json:"type"`
		Seq  uint64 `json:"seq"`
	}
	if err := json.Unmarshal(frame, &env); err != nil {
		return 0, false
	}
	return env.Seq, env.Type == "event"
}
