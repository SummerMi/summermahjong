package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
	redisstore "summermahjong/riichi/internal/store/redis"
	"summermahjong/riichi/internal/store/storetest"
)

// newTestStore 用 miniredis 起一个纯 Go 的假 Redis，并返回接上它的 Store。
func newTestStore(t *testing.T) *redisstore.Store {
	t.Helper()
	s, _ := newTestStoreWithClient(t)
	return s
}

// newTestStoreWithClient 同上，但把底层 redis client 也返回，便于直接构造脏数据。
func newTestStoreWithClient(t *testing.T) (*redisstore.Store, *goredis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return redisstore.New(client), client
}

// newClient 用给定假 Redis 地址新建一个连接（模拟「进程重启后重新连 Redis」）。
func newClient(t *testing.T, mr *miniredis.Miniredis) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func sampleMeta(id string) store.GameMeta {
	return store.GameMeta{
		ID:        id,
		Seed:      42,
		Rules:     game.DefaultRuleset(),
		Agents:    []string{"random", "random", "random", "random"},
		CreatedAt: time.Unix(0, 0).UTC(),
	}
}

// sampleEvents 覆盖了 Event 的全部字段：Kind/Seat/Tile/TileRed/Payload。
func sampleEvents() []game.Event {
	return []game.Event{
		{
			Seq:     1,
			Kind:    game.EventRoundStart,
			Seat:    0,
			Payload: json.RawMessage(`{"round_wind":0,"kyoku":1,"honba":0,"dealer":0}`),
		},
		{Seq: 2, Kind: game.EventDraw, Seat: 0, Tile: "5m"},
		{Seq: 3, Kind: game.EventDiscard, Seat: 0, Tile: "5m"},
		{Seq: 4, Kind: game.EventDraw, Seat: 1, Tile: "0p", TileRed: true},
		{
			Seq:     5,
			Kind:    game.EventDiscard,
			Seat:    1,
			Tile:    "0p",
			TileRed: true,
			Payload: json.RawMessage(`{"tsumogiri":false}`),
		},
	}
}

// TestStoreContract 让 Redis 实现跑与 memory 完全相同的契约套件（契约 §5 要求）。
func TestStoreContract(t *testing.T) {
	storetest.Run(t, func() store.Store { return newTestStore(t) })
}

// TestAppendEventsIdempotent 重复追加同一批事件不会重复插入；部分重叠只补缺失项。
func TestAppendEventsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateGame(ctx, sampleMeta("g-idem")); err != nil {
		t.Fatalf("create: %v", err)
	}
	events := sampleEvents()
	if err := s.AppendEvents(ctx, "g-idem", events); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.AppendEvents(ctx, "g-idem", events); err != nil {
		t.Fatalf("append duplicate batch: %v", err)
	}
	got, err := s.LoadEvents(ctx, "g-idem", 0)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("duplicate append changed stream: got %d events want %d", len(got), len(events))
	}

	// 部分重叠（seq 4、5 已存在，只应补 6）
	tail := []game.Event{
		{Seq: 4, Kind: game.EventDraw, Seat: 1, Tile: "0p", TileRed: true},
		{Seq: 5, Kind: game.EventDiscard, Seat: 1, Tile: "0p", TileRed: true},
		{Seq: 6, Kind: game.EventDraw, Seat: 2, Tile: "9s"},
	}
	if err := s.AppendEvents(ctx, "g-idem", tail); err != nil {
		t.Fatalf("append overlap: %v", err)
	}
	got, err = s.LoadEvents(ctx, "g-idem", 0)
	if err != nil {
		t.Fatalf("load after overlap: %v", err)
	}
	if len(got) != 6 || got[5].Seq != 6 || got[5].Tile != "9s" {
		t.Fatalf("overlap append = %+v", got)
	}
}

// TestLoadEventsFromSeq 校验 fromSeq 截取、升序与边界（fromSeq >= 最大 seq 返回空）。
func TestLoadEventsFromSeq(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateGame(ctx, sampleMeta("g-seq")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AppendEvents(ctx, "g-seq", sampleEvents()); err != nil {
		t.Fatalf("append: %v", err)
	}

	all, err := s.LoadEvents(ctx, "g-seq", 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("from 0: len=%d err=%v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Seq >= all[i].Seq {
			t.Fatalf("events not ascending: %+v", all)
		}
	}
	tail, err := s.LoadEvents(ctx, "g-seq", 3)
	if err != nil {
		t.Fatalf("from 3: %v", err)
	}
	if len(tail) != 2 || tail[0].Seq != 4 || tail[1].Seq != 5 {
		t.Fatalf("from 3 = %+v", tail)
	}
	for _, from := range []uint64{5, 6, 100} {
		got, err := s.LoadEvents(ctx, "g-seq", from)
		if err != nil {
			t.Fatalf("from %d: %v", from, err)
		}
		if len(got) != 0 {
			t.Fatalf("from %d should be empty, got %+v", from, got)
		}
	}
}

// TestLoadEventsEmptyGame 新建但还没有事件的对局应返回空列表（不是 ErrGameNotFound）。
func TestLoadEventsEmptyGame(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateGame(ctx, sampleMeta("g-empty")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.LoadEvents(ctx, "g-empty", 0)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no events, got %+v", got)
	}
	if err := s.AppendEvents(ctx, "g-empty", nil); err != nil {
		t.Fatalf("append empty: %v", err)
	}
}

// TestRestartReplay 模拟进程重启：新连接 / 新 Store 全量 LoadEvents 仍能复盘，快照可续。
func TestRestartReplay(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)

	first := redisstore.New(newClient(t, mr))
	meta := sampleMeta("g-restart")
	if err := first.CreateGame(ctx, meta); err != nil {
		t.Fatalf("create: %v", err)
	}
	events := sampleEvents()
	if err := first.AppendEvents(ctx, meta.ID, events); err != nil {
		t.Fatalf("append: %v", err)
	}
	snapshot := []byte(`{"phase":"await_discard","seq":3}`)
	if err := first.SaveSnapshot(ctx, meta.ID, 3, snapshot); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// 「重启」：全新 client + 全新 Store，只靠 Redis 里的数据恢复。
	second := redisstore.New(newClient(t, mr))
	gotMeta, err := second.GetMeta(ctx, meta.ID)
	if err != nil {
		t.Fatalf("get meta after restart: %v", err)
	}
	if gotMeta.ID != meta.ID || gotMeta.Seed != meta.Seed ||
		gotMeta.Rules != meta.Rules || !gotMeta.CreatedAt.Equal(meta.CreatedAt) ||
		!reflect.DeepEqual(gotMeta.Agents, meta.Agents) {
		t.Fatalf("meta after restart = %+v want %+v", gotMeta, meta)
	}

	replayed, err := second.LoadEvents(ctx, meta.ID, 0)
	if err != nil {
		t.Fatalf("load events after restart: %v", err)
	}
	if !reflect.DeepEqual(replayed, events) {
		t.Fatalf("replay mismatch:\n got %+v\nwant %+v", replayed, events)
	}
	snap, at, ok, err := second.LoadSnapshot(ctx, meta.ID)
	if err != nil || !ok {
		t.Fatalf("load snapshot after restart: ok=%v err=%v", ok, err)
	}
	if at != 3 || string(snap) != string(snapshot) {
		t.Fatalf("snapshot after restart = at=%d body=%s", at, snap)
	}

	// 重启后继续追加：旧 Store 也应立即读到（同一份 Redis 数据）。
	if err := second.AppendEvents(ctx, meta.ID, []game.Event{
		{Seq: 6, Kind: game.EventDraw, Seat: 2, Tile: "1s"},
	}); err != nil {
		t.Fatalf("append after restart: %v", err)
	}
	fromFirst, err := first.LoadEvents(ctx, meta.ID, 5)
	if err != nil {
		t.Fatalf("load from old store: %v", err)
	}
	if len(fromFirst) != 1 || fromFirst[0].Seq != 6 || fromFirst[0].Tile != "1s" {
		t.Fatalf("incremental load after restart = %+v", fromFirst)
	}
}

// TestSnapshotLatestWins 快照多次保存后 LoadSnapshot 返回最后一次（atSeq 同步更新）。
func TestSnapshotLatestWins(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateGame(ctx, sampleMeta("g-snap")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SaveSnapshot(ctx, "g-snap", 3, []byte("snap-3")); err != nil {
		t.Fatalf("save 3: %v", err)
	}
	if err := s.SaveSnapshot(ctx, "g-snap", 7, []byte("snap-7")); err != nil {
		t.Fatalf("save 7: %v", err)
	}
	got, at, ok, err := s.LoadSnapshot(ctx, "g-snap")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if at != 7 || string(got) != "snap-7" {
		t.Fatalf("latest snapshot = at=%d body=%s", at, got)
	}
}

// TestCreateGameKeepsFirstMeta 重复建同一局不覆盖首次元数据（与 Memory 语义一致）。
func TestCreateGameKeepsFirstMeta(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateGame(ctx, sampleMeta("g-meta")); err != nil {
		t.Fatalf("create: %v", err)
	}
	other := sampleMeta("g-meta")
	other.Seed = 99
	if err := s.CreateGame(ctx, other); err != nil {
		t.Fatalf("create again: %v", err)
	}
	got, err := s.GetMeta(ctx, "g-meta")
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if got.Seed != 42 {
		t.Fatalf("second create overwrote meta: seed=%d want 42", got.Seed)
	}
}

// TestMetaRoundTrip 校验 Rules / Agents / CreatedAt（含纳秒）完整回读。
func TestMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	meta := store.GameMeta{
		ID:        "g-meta-full",
		Seed:      -7,
		Rules:     game.Ruleset{RedFives: 2, StartingPoints: 30000},
		Agents:    []string{"random", "heuristic", "mcp", "random"},
		CreatedAt: time.Date(2026, time.November, 10, 12, 34, 56, 789123456, time.UTC),
	}
	if err := s.CreateGame(ctx, meta); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetMeta(ctx, meta.ID)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if got.ID != meta.ID || got.Seed != meta.Seed || got.Rules != meta.Rules {
		t.Fatalf("meta mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.Agents, meta.Agents) {
		t.Fatalf("agents mismatch: %+v", got.Agents)
	}
	if !got.CreatedAt.Equal(meta.CreatedAt) {
		t.Fatalf("created_at mismatch: got %v want %v", got.CreatedAt, meta.CreatedAt)
	}
}

// TestKeyLayout 校验键命名与契约一致（便于运维 / 面试时直接 redis-cli 查看）。
func TestKeyLayout(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := newClient(t, mr)
	s := redisstore.New(client)
	if err := s.CreateGame(ctx, sampleMeta("layout")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AppendEvents(ctx, "layout", sampleEvents()[:1]); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.SaveSnapshot(ctx, "layout", 1, []byte("snap")); err != nil {
		t.Fatalf("save: %v", err)
	}
	keys := map[string]bool{}
	for _, key := range mr.Keys() {
		keys[key] = true
	}
	for _, want := range []string{
		"riichi:meta:layout",
		"riichi:events:layout",
		"riichi:snap:layout",
		"riichi:snapseq:layout",
	} {
		if !keys[want] {
			t.Fatalf("missing key %q, got %v", want, mr.Keys())
		}
	}
}

// TestUnknownGameReturnsErrGameNotFound 所有接口对未知对局都返回哨兵错误（transport 映射 404）。
func TestUnknownGameReturnsErrGameNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	checks := map[string]error{
		"get meta": func() error { _, err := s.GetMeta(ctx, "ghost"); return err }(),
		"load events": func() error {
			_, err := s.LoadEvents(ctx, "ghost", 0)
			return err
		}(),
		"append events": s.AppendEvents(ctx, "ghost", []game.Event{{Seq: 1}}),
		"save snapshot": s.SaveSnapshot(ctx, "ghost", 1, []byte("x")),
		"load snapshot": func() error {
			_, _, _, err := s.LoadSnapshot(ctx, "ghost")
			return err
		}(),
	}
	for name, err := range checks {
		if !errors.Is(err, store.ErrGameNotFound) {
			t.Fatalf("%s: want ErrGameNotFound, got %v", name, err)
		}
	}
}

// TestAppendEventsDuplicateSeqInOneBatch 同一批里出现重复 seq 时只落一条（幂等）；
// 这条路径同时覆盖「XADD 因 ID 已存在失败 → 确认已存在 → 视为成功」的并发兜底分支。
func TestAppendEventsDuplicateSeqInOneBatch(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateGame(ctx, sampleMeta("g-dup")); err != nil {
		t.Fatalf("create: %v", err)
	}
	dup := []game.Event{
		{Seq: 1, Kind: game.EventDraw, Seat: 0, Tile: "1m"},
		{Seq: 1, Kind: game.EventDraw, Seat: 0, Tile: "1m"},
	}
	if err := s.AppendEvents(ctx, "g-dup", dup); err != nil {
		t.Fatalf("append duplicate seq in one batch: %v", err)
	}
	got, err := s.LoadEvents(ctx, "g-dup", 0)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("want single seq=1 event, got %+v", got)
	}
}

// TestCorruptedMetaReturnsError 元数据被写坏时返回错误，而不是给出错误值。
func TestCorruptedMetaReturnsError(t *testing.T) {
	ctx := context.Background()
	s, client := newTestStoreWithClient(t)
	if err := s.CreateGame(ctx, sampleMeta("g-bad-meta")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := client.HSet(ctx, "riichi:meta:g-bad-meta", "seed", "not-a-number").Err(); err != nil {
		t.Fatalf("corrupt meta: %v", err)
	}
	if _, err := s.GetMeta(ctx, "g-bad-meta"); err == nil {
		t.Fatal("GetMeta should fail on corrupted meta")
	}
}

// TestCorruptedEventReturnsError 事件流里出现坏 entry 时返回错误（不静默跳过）。
func TestCorruptedEventReturnsError(t *testing.T) {
	ctx := context.Background()
	s, client := newTestStoreWithClient(t)
	if err := s.CreateGame(ctx, sampleMeta("g-bad-event")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := client.XAdd(ctx, &goredis.XAddArgs{
		Stream: "riichi:events:g-bad-event",
		ID:     "50-0",
		Values: map[string]interface{}{"seq": "oops", "kind": "draw", "seat": "0"},
	}).Err(); err != nil {
		t.Fatalf("inject bad event: %v", err)
	}
	if _, err := s.LoadEvents(ctx, "g-bad-event", 0); err == nil {
		t.Fatal("LoadEvents should fail on corrupted event")
	}
}

// TestRedisDownReturnsError Redis 不可用时返回错误（而不是 panic / 假装成功）。
func TestRedisDownReturnsError(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	// 关掉重试 / 缩短拨号超时：Redis 已关闭时避免 go-redis 默认重试拖慢测试。
	client := goredis.NewClient(&goredis.Options{
		Addr:         mr.Addr(),
		MaxRetries:   -1,
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	s := redisstore.New(client)
	mr.Close() // 模拟 Redis 进程挂掉

	if err := s.CreateGame(ctx, sampleMeta("g-down")); err == nil {
		t.Fatal("CreateGame should fail when redis is down")
	}
	if _, err := s.GetMeta(ctx, "g-down"); err == nil {
		t.Fatal("GetMeta should fail when redis is down")
	}
	if err := s.AppendEvents(ctx, "g-down", sampleEvents()); err == nil {
		t.Fatal("AppendEvents should fail when redis is down")
	}
}
