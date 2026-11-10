// Package redis 是 store.Store 的 Redis 实现（S7 持久化）。
//
// 键布局（对应 docs/riichi-接口契约-20261110.md §5 与 task-7）：
//
//	riichi:meta:{game_id}     Hash    元数据：id / seed / rules / agents / created_at
//	riichi:events:{game_id}   Stream  事件流：entry ID = "{seq+1}-0"，字段 seq/kind/seat/tile/tile_red/payload
//	riichi:snap:{game_id}     String  状态快照（调用方自行序列化，本包只当不透明字节）
//	riichi:snapseq:{game_id}  String  快照对应的 seq
//
// 为什么事件用 Redis Stream：XADD/XRANGE 天然按 entry ID 有序，LoadEvents(fromSeq)
// 只要给出起点 ID 就能截取后缀，不需要在客户端排序。entry ID 取 "{seq+1}-0" 而不是
// "{seq}-0"，一是避开 Redis 保留的非法 ID "0-0"（seq=0 也合法），二是 ID 的数值序
// 与 seq 升序一致（ID 的毫秒段按数值比较，不是字符串比较）。
//
// 幂等：AppendEvents 先用一次 pipeline 的 XRANGE 检查每个 seq 是否已存在，只追加缺失项，
// 所以「重复提交同一批事件」不会重复插入。并发重复追加时 XADD 会因 ID 不够大而报错，
// 此时再确认该 seq 已经存在，即视为幂等成功（而不是把错误抛给调用方）。
//
// 快照与元数据都是 key 级别的原子写：SaveSnapshot 用 TxPipeline 同时写 snap + snapseq，
// 不会出现「快照更新了、seq 没更新」的中间态。
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

const (
	metaKeyPrefix     = "riichi:meta:"
	eventsKeyPrefix   = "riichi:events:"
	snapshotKeyPrefix = "riichi:snap:"
	snapshotSeqPrefix = "riichi:snapseq:"
)

// 编译期确认接口实现，避免签名漂移。
var _ store.Store = (*Store)(nil)

// Store 是基于 Redis 的 store.Store 实现。
//
// 它不做任何业务校验（事件合法性、seq 单调性由调用方保证），只负责把
// 「元数据 / 事件流 / 快照」按契约落盘；因此可以直接复用 storetest 契约套件。
type Store struct {
	client goredis.UniversalClient
}

// New 用给定 Redis 客户端构造 Store。
//
// 参数用 UniversalClient 而不是 *Client，是为了同时兼容单机 / Cluster 客户端，
// 测试里也可以直接传 miniredis 支持的普通客户端。
func New(client goredis.UniversalClient) *Store {
	return &Store{client: client}
}

func gameMetaKey(gameID string) string     { return metaKeyPrefix + gameID }
func gameEventsKey(gameID string) string   { return eventsKeyPrefix + gameID }
func gameSnapshotKey(gameID string) string { return snapshotKeyPrefix + gameID }
func gameSnapSeqKey(gameID string) string  { return snapshotSeqPrefix + gameID }

// streamID 把事件 seq 映射为 Stream entry ID。
//
// seq=0 映射成 "1-0"（避开非法 ID "0-0"），且 seq 越大 ID 越大。
func streamID(seq uint64) string {
	return strconv.FormatUint(seq+1, 10) + "-0"
}

// CreateGame 写入元数据；已存在时保持首次写入的内容并返回 nil（与 Memory 语义一致）。
func (s *Store) CreateGame(ctx context.Context, meta store.GameMeta) error {
	added, err := s.client.HSetNX(ctx, gameMetaKey(meta.ID), "id", meta.ID).Result()
	if err != nil {
		return fmt.Errorf("redis store: create game %q: %w", meta.ID, err)
	}
	if !added {
		return nil // 幂等：已存在不覆盖（Memory 也是只写第一次）
	}
	fields, err := metaFields(meta)
	if err != nil {
		return fmt.Errorf("redis store: create game %q: %w", meta.ID, err)
	}
	if err := s.client.HSet(ctx, gameMetaKey(meta.ID), fields).Err(); err != nil {
		return fmt.Errorf("redis store: create game %q: %w", meta.ID, err)
	}
	return nil
}

// metaFields 把 GameMeta 编码成 Hash 字段（结构体字段走 JSON，时间用 RFC3339Nano）。
func metaFields(meta store.GameMeta) (map[string]interface{}, error) {
	rules, err := json.Marshal(meta.Rules)
	if err != nil {
		return nil, fmt.Errorf("marshal rules: %w", err)
	}
	agents, err := json.Marshal(meta.Agents)
	if err != nil {
		return nil, fmt.Errorf("marshal agents: %w", err)
	}
	return map[string]interface{}{
		"id":         meta.ID,
		"seed":       strconv.FormatInt(meta.Seed, 10),
		"rules":      string(rules),
		"agents":     string(agents),
		"created_at": meta.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// GetMeta 实现 Store。
func (s *Store) GetMeta(ctx context.Context, gameID string) (store.GameMeta, error) {
	raw, err := s.client.HGetAll(ctx, gameMetaKey(gameID)).Result()
	if err != nil {
		return store.GameMeta{}, fmt.Errorf("redis store: get meta %q: %w", gameID, err)
	}
	if len(raw) == 0 {
		return store.GameMeta{}, store.ErrGameNotFound
	}
	return decodeMeta(raw)
}

func decodeMeta(raw map[string]string) (store.GameMeta, error) {
	seed, err := strconv.ParseInt(raw["seed"], 10, 64)
	if err != nil {
		return store.GameMeta{}, fmt.Errorf("redis store: bad meta seed %q: %w", raw["seed"], err)
	}
	var rules game.Ruleset
	if err := json.Unmarshal([]byte(raw["rules"]), &rules); err != nil {
		return store.GameMeta{}, fmt.Errorf("redis store: bad meta rules: %w", err)
	}
	var agents []string
	if err := json.Unmarshal([]byte(raw["agents"]), &agents); err != nil {
		return store.GameMeta{}, fmt.Errorf("redis store: bad meta agents: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, raw["created_at"])
	if err != nil {
		return store.GameMeta{}, fmt.Errorf("redis store: bad meta created_at %q: %w", raw["created_at"], err)
	}
	return store.GameMeta{
		ID:        raw["id"],
		Seed:      seed,
		Rules:     rules,
		Agents:    agents,
		CreatedAt: createdAt,
	}, nil
}

// AppendEvents 追加事件；重复的 seq 不会重复插入（幂等）。
//
// 实现分两步：先一次 pipeline 查哪些 seq 已存在，再只 XADD 缺失项。
// 第二步若遇到「ID 不够大」，说明并发写者抢先插入了同一批事件，逐个确认已存在
// 即视为成功；确认不存在才把底层错误返回。
func (s *Store) AppendEvents(ctx context.Context, gameID string, events []game.Event) error {
	if err := s.ensureGame(ctx, gameID); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	key := gameEventsKey(gameID)

	check := s.client.Pipeline()
	found := make([]*goredis.XMessageSliceCmd, len(events))
	for i, ev := range events {
		id := streamID(ev.Seq)
		found[i] = check.XRangeN(ctx, key, id, id, 1)
	}
	if _, err := check.Exec(ctx); err != nil {
		return fmt.Errorf("redis store: check events %q: %w", gameID, err)
	}
	missing := make([]game.Event, 0, len(events))
	for i, ev := range events {
		msgs, err := found[i].Result()
		if err != nil {
			return fmt.Errorf("redis store: check event seq=%d game=%q: %w", ev.Seq, gameID, err)
		}
		if len(msgs) == 0 {
			missing = append(missing, ev)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	add := s.client.Pipeline()
	added := make([]*goredis.StringCmd, len(missing))
	for i, ev := range missing {
		added[i] = add.XAdd(ctx, &goredis.XAddArgs{
			Stream: key,
			ID:     streamID(ev.Seq),
			Values: eventValues(ev),
		})
	}
	if _, err := add.Exec(ctx); err != nil {
		for i, ev := range missing {
			if added[i].Err() == nil {
				continue // 这条其实写成功了（同批其它命令失败不影响它）
			}
			exists, checkErr := s.eventExists(ctx, key, ev.Seq)
			if checkErr != nil {
				return fmt.Errorf("redis store: append events %q: %w", gameID, checkErr)
			}
			if !exists {
				return fmt.Errorf("redis store: append event seq=%d game=%q: %w", ev.Seq, gameID, added[i].Err())
			}
		}
	}
	return nil
}

// eventValues 把事件编码成 XADD 的 field/value 列表。
func eventValues(ev game.Event) map[string]interface{} {
	values := map[string]interface{}{
		"seq":  strconv.FormatUint(ev.Seq, 10),
		"kind": string(ev.Kind),
		"seat": strconv.Itoa(int(ev.Seat)),
	}
	if ev.Tile != "" {
		values["tile"] = ev.Tile
	}
	if ev.TileRed {
		values["tile_red"] = "1"
	}
	if len(ev.Payload) > 0 {
		values["payload"] = string(ev.Payload)
	}
	return values
}

// eventExists 判断某个 seq 对应的事件是否已写入 Stream。
func (s *Store) eventExists(ctx context.Context, key string, seq uint64) (bool, error) {
	id := streamID(seq)
	msgs, err := s.client.XRangeN(ctx, key, id, id, 1).Result()
	if err != nil {
		return false, err
	}
	return len(msgs) > 0, nil
}

// LoadEvents 返回 seq > fromSeq 的事件，按 seq 升序（Stream 的 ID 序即 seq 序）。
func (s *Store) LoadEvents(ctx context.Context, gameID string, fromSeq uint64) ([]game.Event, error) {
	if err := s.ensureGame(ctx, gameID); err != nil {
		return nil, err
	}
	if fromSeq == math.MaxUint64 {
		return []game.Event{}, nil
	}
	// seq > fromSeq 的第一条事件对应 ID (fromSeq+2)-0；这里把起点放在 (fromSeq+1)-0
	// 会多读一条 seq == fromSeq 的旧事件，下面按 seq > fromSeq 过滤掉即可（少一次边界推导）。
	msgs, err := s.client.XRangeN(ctx, gameEventsKey(gameID), streamID(fromSeq), "+", 0).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: load events %q: %w", gameID, err)
	}
	events := make([]game.Event, 0, len(msgs))
	for _, msg := range msgs {
		ev, err := decodeEvent(msg)
		if err != nil {
			return nil, fmt.Errorf("redis store: load events %q: %w", gameID, err)
		}
		if ev.Seq <= fromSeq {
			continue // 起点多读的那一条（seq == fromSeq）
		}
		events = append(events, ev)
	}
	return events, nil
}

// decodeEvent 把 Stream entry 还原成 game.Event。
func decodeEvent(msg goredis.XMessage) (game.Event, error) {
	field := func(name string) string {
		v, ok := msg.Values[name]
		if !ok || v == nil {
			return ""
		}
		switch value := v.(type) {
		case string:
			return value
		case []byte:
			return string(value)
		default:
			return fmt.Sprint(value)
		}
	}
	seq, err := strconv.ParseUint(field("seq"), 10, 64)
	if err != nil {
		return game.Event{}, fmt.Errorf("bad event seq %q: %w", field("seq"), err)
	}
	seat, err := strconv.ParseUint(field("seat"), 10, 8)
	if err != nil {
		return game.Event{}, fmt.Errorf("bad event seat %q: %w", field("seat"), err)
	}
	ev := game.Event{
		Seq:     seq,
		Kind:    game.EventKind(field("kind")),
		Seat:    game.Seat(seat),
		Tile:    field("tile"),
		TileRed: field("tile_red") == "1",
	}
	if payload := field("payload"); payload != "" {
		ev.Payload = json.RawMessage(payload)
	}
	return ev, nil
}

// SaveSnapshot 保存快照与对应 seq（TxPipeline 保证两个 key 同时更新）。
func (s *Store) SaveSnapshot(ctx context.Context, gameID string, atSeq uint64, snapshot []byte) error {
	if err := s.ensureGame(ctx, gameID); err != nil {
		return err
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, gameSnapshotKey(gameID), snapshot, 0)
	pipe.Set(ctx, gameSnapSeqKey(gameID), strconv.FormatUint(atSeq, 10), 0)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis store: save snapshot %q: %w", gameID, err)
	}
	return nil
}

// LoadSnapshot 返回最新快照；没有快照时 ok=false。
func (s *Store) LoadSnapshot(ctx context.Context, gameID string) ([]byte, uint64, bool, error) {
	if err := s.ensureGame(ctx, gameID); err != nil {
		return nil, 0, false, err
	}
	values, err := s.client.MGet(ctx, gameSnapshotKey(gameID), gameSnapSeqKey(gameID)).Result()
	if err != nil {
		return nil, 0, false, fmt.Errorf("redis store: load snapshot %q: %w", gameID, err)
	}
	if len(values) < 2 || values[0] == nil {
		return nil, 0, false, nil
	}
	var snapshot []byte
	switch value := values[0].(type) {
	case string:
		snapshot = []byte(value)
	case []byte:
		snapshot = value
	default:
		return nil, 0, false, fmt.Errorf("redis store: unexpected snapshot type %T", values[0])
	}
	var atSeq uint64
	if values[1] != nil {
		text, ok := values[1].(string)
		if !ok {
			return nil, 0, false, fmt.Errorf("redis store: unexpected snapshot seq type %T", values[1])
		}
		atSeq, err = strconv.ParseUint(text, 10, 64)
		if err != nil {
			return nil, 0, false, fmt.Errorf("redis store: bad snapshot seq %q: %w", text, err)
		}
	}
	return snapshot, atSeq, true, nil
}

// ensureGame 检查元数据是否存在，不存在返回 store.ErrGameNotFound。
func (s *Store) ensureGame(ctx context.Context, gameID string) error {
	n, err := s.client.Exists(ctx, gameMetaKey(gameID)).Result()
	if err != nil {
		return fmt.Errorf("redis store: check game %q: %w", gameID, err)
	}
	if n == 0 {
		return store.ErrGameNotFound
	}
	return nil
}
