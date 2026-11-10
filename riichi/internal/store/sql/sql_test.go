// 本文件是 MySQL 实现的集成测试：既有 storetest 契约套件，也有「重启后回放」等专项用例。
//
// 需要真实 MySQL 时会读环境变量 MYSQL_TEST_URL，形如：
//
//	MYSQL_TEST_URL="root@tcp(127.0.0.1:3307)/riichi_test?parseTime=true&loc=UTC&charset=utf8mb4"
//
// 库需预先创建（测试只会 DROP / CREATE 表，不会删库）：
//
//	CREATE DATABASE IF NOT EXISTS riichi_test CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
//
// 未设置 MYSQL_TEST_URL 时整组用例自动 skip，默认 `go test ./...` 仍然零依赖通过；
// 不引入 sqlite / CGO 依赖，保证 Windows / CI 上都能跑。
package sqlstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sqlstore "summermahjong/riichi/internal/store/sql"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
	"summermahjong/riichi/internal/store/storetest"
)

// mysqlDSN 返回测试 DSN；没有 MySQL 环境时直接 skip。
func mysqlDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MYSQL_TEST_URL")
	if dsn == "" {
		t.Skip("未设置 MYSQL_TEST_URL，跳过 MySQL 集成测试（默认套件保持零依赖）")
	}
	return dsn
}

// openTestStore 打开一个 Store（自带连接池），测试结束自动 Close。
func openTestStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	s, err := sqlstore.Open(context.Background(), mysqlDSN(t))
	if err != nil {
		t.Fatalf("打开 MySQL: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// resetSchema 让用例从三张空表开始：先 DROP（外键先子表后父表）再 Migrate。
func resetSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"game_events", "snapshots", "games"} {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("清理表 %s: %v", table, err)
		}
	}
	if err := sqlstore.Migrate(ctx, db); err != nil {
		t.Fatalf("建表: %v", err)
	}
}

// sampleMeta 构造一份带微秒时间戳的元数据，用来验证元数据字段的完整往返。
func sampleMeta(id string) store.GameMeta {
	return store.GameMeta{
		ID:        id,
		Seed:      1_725_000_000,
		Rules:     game.DefaultRuleset(),
		Agents:    []string{"random", "heuristic", "random", "random"},
		CreatedAt: time.Date(2026, 11, 10, 12, 30, 45, 123456000, time.UTC),
	}
}

// sampleEvents 覆盖 M1 事件的全部字段：Seq / Kind / Seat / Tile / TileRed / Payload。
func sampleEvents() []game.Event {
	return []game.Event{
		{Seq: 1, Kind: game.EventRoundStart, Seat: 0, Payload: json.RawMessage(`{"round_wind":0,"kyoku":0}`)},
		{Seq: 2, Kind: game.EventDraw, Seat: 0, Tile: "5m", TileRed: true},
		{Seq: 3, Kind: game.EventDiscard, Seat: 0, Tile: "5m"},
		{Seq: 4, Kind: game.EventDraw, Seat: 1, Tile: "3p", Payload: json.RawMessage(`{"wall_remaining":121}`)},
		{Seq: 5, Kind: game.EventDiscard, Seat: 1, Tile: "3p", Payload: json.RawMessage(`{"tsumogiri":false}`)},
	}
}

// normalizePayload 把事件载荷解析成通用 JSON 值。
//
// MySQL 的 JSON 列会归一化 JSON（去空白、重排对象 key），所以 LoadEvents 读回的 Payload
// 与写入时**语义相同、字节可能不同**；比较事件时统一走这个函数。
func normalizePayload(t *testing.T, event game.Event) any {
	t.Helper()
	if len(event.Payload) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(event.Payload, &value); err != nil {
		t.Fatalf("事件 seq=%d 的 Payload 不是合法 JSON: %v", event.Seq, err)
	}
	return value
}

// eventsEqual 按「字段精确 + Payload 语义」比较两组事件，顺序必须一致。
func eventsEqual(t *testing.T, got, want []game.Event) bool {
	t.Helper()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Seq != w.Seq || g.Kind != w.Kind || g.Seat != w.Seat || g.Tile != w.Tile || g.TileRed != w.TileRed {
			return false
		}
		if !reflect.DeepEqual(normalizePayload(t, g), normalizePayload(t, w)) {
			return false
		}
	}
	return true
}

// TestContractSuite 是契约套件：memory / sql / redis 三种实现必须跑同一套（契约 §5、§11 S7）。
func TestContractSuite(t *testing.T) {
	s := openTestStore(t)
	resetSchema(t, s.DB())
	storetest.Run(t, func() store.Store { return sqlstore.New(s.DB()) })
}

// TestMigrateIsIdempotentAndSchemaMatchesContract 验证建表幂等 + 表结构符合约定。
func TestMigrateIsIdempotentAndSchemaMatchesContract(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	resetSchema(t, s.DB())

	// 幂等：再来一次不应报错（CREATE TABLE IF NOT EXISTS）
	if err := sqlstore.Migrate(ctx, s.DB()); err != nil {
		t.Fatalf("重复 Migrate 应当幂等: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Store.Migrate: %v", err)
	}

	// 三张表 + 存储引擎
	rows, err := s.DB().QueryContext(ctx, `
		SELECT TABLE_NAME, ENGINE FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('games','game_events','snapshots')`)
	if err != nil {
		t.Fatalf("查询表: %v", err)
	}
	defer func() { _ = rows.Close() }()
	engines := map[string]string{}
	for rows.Next() {
		var name, engine string
		if err := rows.Scan(&name, &engine); err != nil {
			t.Fatalf("扫描表: %v", err)
		}
		engines[name] = engine
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表: %v", err)
	}
	for _, table := range []string{"games", "game_events", "snapshots"} {
		if engines[table] != "InnoDB" {
			t.Fatalf("表 %s 的引擎应为 InnoDB，实际 %q", table, engines[table])
		}
	}

	// 关键列的类型
	columnType := func(table, column string) string {
		t.Helper()
		var columnType string
		err := s.DB().QueryRowContext(ctx, `
			SELECT COLUMN_TYPE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
			table, column,
		).Scan(&columnType)
		if err != nil {
			t.Fatalf("查询列 %s.%s: %v", table, column, err)
		}
		return columnType
	}
	if got := columnType("game_events", "payload"); got != "json" {
		t.Fatalf("game_events.payload 应为 json，实际 %q", got)
	}
	if got := columnType("games", "rules_json"); got != "json" {
		t.Fatalf("games.rules_json 应为 json，实际 %q", got)
	}
	if got := columnType("snapshots", "body"); got != "blob" {
		t.Fatalf("snapshots.body 应为 blob，实际 %q", got)
	}

	// 事件表主键必须是 (game_id, seq) 且顺序正确
	pkRows, err := s.DB().QueryContext(ctx, `
		SELECT COLUMN_NAME, SEQ_IN_INDEX FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'game_events' AND INDEX_NAME = 'PRIMARY'
		ORDER BY SEQ_IN_INDEX`)
	if err != nil {
		t.Fatalf("查询主键: %v", err)
	}
	defer func() { _ = pkRows.Close() }()
	var pk []string
	for pkRows.Next() {
		var column string
		var position int
		if err := pkRows.Scan(&column, &position); err != nil {
			t.Fatalf("扫描主键: %v", err)
		}
		pk = append(pk, column)
	}
	if !reflect.DeepEqual(pk, []string{"game_id", "seq"}) {
		t.Fatalf("game_events 主键应为 (game_id, seq)，实际 %v", pk)
	}
}

// TestEventsSurviveNewStoreInstance 是「重启后回放」用例：
// 写事件 → 关掉旧 Store → 新建 Store（新进程视图）→ 全量 LoadEvents → 与写入逐字段一致。
func TestEventsSurviveNewStoreInstance(t *testing.T) {
	ctx := context.Background()
	first := openTestStore(t)
	resetSchema(t, first.DB())

	meta := sampleMeta("restart-1")
	events := sampleEvents()
	snapshot := []byte(`{"seq":5,"phase":"AwaitDraw"}`)

	if err := first.CreateGame(ctx, meta); err != nil {
		t.Fatalf("创建对局: %v", err)
	}
	if err := first.AppendEvents(ctx, "restart-1", events); err != nil {
		t.Fatalf("追加事件: %v", err)
	}
	if err := first.SaveSnapshot(ctx, "restart-1", 5, snapshot); err != nil {
		t.Fatalf("保存快照: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("关闭旧连接池: %v", err)
	}

	// 新 Store = 新连接池 = 模拟进程重启后重新从 MySQL 读
	second, err := sqlstore.Open(ctx, mysqlDSN(t))
	if err != nil {
		t.Fatalf("重启后重新连接: %v", err)
	}
	defer func() { _ = second.Close() }()

	got, err := second.LoadEvents(ctx, "restart-1", 0)
	if err != nil {
		t.Fatalf("回放加载事件: %v", err)
	}
	if !eventsEqual(t, got, events) {
		t.Fatalf("重启后事件不一致\n got: %+v\nwant: %+v", got, events)
	}

	// payload 列是真正的 JSON：能直接用 JSON_EXTRACT 按事件载荷查询（不只是字符串存储）
	var kyoku sql.NullInt64
	if err := second.DB().QueryRowContext(ctx,
		`SELECT JSON_EXTRACT(payload, '$.payload.kyoku')
		   FROM game_events WHERE game_id = 'restart-1' AND seq = 1`,
	).Scan(&kyoku); err != nil {
		t.Fatalf("JSON_EXTRACT 查询事件载荷: %v", err)
	}
	if !kyoku.Valid || kyoku.Int64 != 0 {
		t.Fatalf("round_start 载荷里的 kyoku 应为 0，实际 %+v", kyoku)
	}

	// fromSeq 过滤 + 边界
	tail, err := second.LoadEvents(ctx, "restart-1", 3)
	if err != nil {
		t.Fatalf("按 seq 加载: %v", err)
	}
	if !eventsEqual(t, tail, events[3:]) {
		t.Fatalf("seq>3 应返回最后两条，实际 %+v", tail)
	}
	if empty, err := second.LoadEvents(ctx, "restart-1", 5); err != nil || len(empty) != 0 {
		t.Fatalf("seq>最大 应返回空，实际 %v err=%v", empty, err)
	}

	// 快照也应当从库里回来
	body, atSeq, ok, err := second.LoadSnapshot(ctx, "restart-1")
	if err != nil || !ok {
		t.Fatalf("读取快照: ok=%v err=%v", ok, err)
	}
	if atSeq != 5 || string(body) != string(snapshot) {
		t.Fatalf("快照不一致 at=%d body=%s", atSeq, body)
	}

	// 元数据逐字段往返（Rules / Agents / CreatedAt 都不能丢）
	gotMeta, err := second.GetMeta(ctx, "restart-1")
	if err != nil {
		t.Fatalf("读取元数据: %v", err)
	}
	if !reflect.DeepEqual(gotMeta, meta) {
		t.Fatalf("元数据不一致\n got: %+v\nwant: %+v", gotMeta, meta)
	}
}

// TestAppendEventsIsIdempotent 验证重复 seq 幂等：重复整批、批内重复都要「首次写入优先」。
func TestAppendEventsIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	resetSchema(t, s.DB())

	if err := s.CreateGame(ctx, sampleMeta("idem-1")); err != nil {
		t.Fatalf("创建对局: %v", err)
	}
	batch := sampleEvents()[:3]
	for round := 0; round < 3; round++ {
		if err := s.AppendEvents(ctx, "idem-1", batch); err != nil {
			t.Fatalf("第 %d 次追加: %v", round+1, err)
		}
	}
	// 再追加 seq=2 的另一份内容 + 一条新事件：seq=2 必须保持首次写入的值
	if err := s.AppendEvents(ctx, "idem-1", []game.Event{
		{Seq: 2, Kind: game.EventDiscard, Seat: 2, Tile: "9s"},
		{Seq: 4, Kind: game.EventDraw, Seat: 2, Tile: "1z"},
	}); err != nil {
		t.Fatalf("追加重复 seq: %v", err)
	}
	got, err := s.LoadEvents(ctx, "idem-1", 0)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("幂等后应有 4 条事件，实际 %d", len(got))
	}
	if !reflect.DeepEqual(got[1], batch[1]) {
		t.Fatalf("重复 seq 应保留首次写入，实际 %+v", got[1])
	}

	// 同一批里出现重复 seq：先出现的那条生效
	if err := s.CreateGame(ctx, sampleMeta("idem-2")); err != nil {
		t.Fatalf("创建对局: %v", err)
	}
	if err := s.AppendEvents(ctx, "idem-2", []game.Event{
		{Seq: 1, Kind: game.EventRoundStart, Seat: 0, Tile: "1m"},
		{Seq: 1, Kind: game.EventDiscard, Seat: 3, Tile: "9p"},
	}); err != nil {
		t.Fatalf("批内重复 seq: %v", err)
	}
	dup, err := s.LoadEvents(ctx, "idem-2", 0)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	if len(dup) != 1 || dup[0].Kind != game.EventRoundStart || dup[0].Tile != "1m" {
		t.Fatalf("批内重复 seq 应保留第一条，实际 %+v", dup)
	}
}

// TestAppendEventsOutOfOrderAndBatches 验证：乱序输入按 seq 升序落库，跨多批也能完整读回。
func TestAppendEventsOutOfOrderAndBatches(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	resetSchema(t, s.DB())
	if err := s.CreateGame(ctx, sampleMeta("batch-1")); err != nil {
		t.Fatalf("创建对局: %v", err)
	}

	// 1203 条，越过 maxEventsPerBatch=500 的边界；输入故意倒序
	const total = 1203
	events := make([]game.Event, 0, total)
	for i := total; i >= 1; i-- {
		events = append(events, game.Event{
			Seq:     uint64(i),
			Kind:    game.EventDraw,
			Seat:    game.Seat(i % 4),
			Tile:    "5m",
			Payload: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
		})
	}
	if err := s.AppendEvents(ctx, "batch-1", events); err != nil {
		t.Fatalf("追加 %d 条事件: %v", total, err)
	}

	got, err := s.LoadEvents(ctx, "batch-1", 0)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	if len(got) != total {
		t.Fatalf("应有 %d 条，实际 %d", total, len(got))
	}
	for i, event := range got {
		if event.Seq != uint64(i+1) {
			t.Fatalf("第 %d 条 seq 应为 %d，实际 %d", i, i+1, event.Seq)
		}
	}
	tail, err := s.LoadEvents(ctx, "batch-1", 500)
	if err != nil {
		t.Fatalf("分页加载: %v", err)
	}
	if len(tail) != total-500 || tail[0].Seq != 501 {
		t.Fatalf("seq>500 应为 703 条且从 501 开始，实际 %d 条 首条 %d", len(tail), tail[0].Seq)
	}
}

// TestUnknownGameErrors 验证错误语义与 Memory 一致（契约 §7 哨兵错误）。
func TestUnknownGameErrors(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	resetSchema(t, s.DB())

	const ghost = "ghost-game"
	if _, err := s.LoadEvents(ctx, ghost, 0); !errors.Is(err, store.ErrGameNotFound) {
		t.Fatalf("LoadEvents 未知对局应为 ErrGameNotFound，实际 %v", err)
	}
	if err := s.AppendEvents(ctx, ghost, []game.Event{{Seq: 1}}); !errors.Is(err, store.ErrGameNotFound) {
		t.Fatalf("AppendEvents 未知对局应为 ErrGameNotFound，实际 %v", err)
	}
	if err := s.AppendEvents(ctx, ghost, nil); !errors.Is(err, store.ErrGameNotFound) {
		t.Fatalf("AppendEvents 空批次未知对局应为 ErrGameNotFound，实际 %v", err)
	}
	if err := s.SaveSnapshot(ctx, ghost, 1, []byte(`{}`)); !errors.Is(err, store.ErrGameNotFound) {
		t.Fatalf("SaveSnapshot 未知对局应为 ErrGameNotFound，实际 %v", err)
	}
	if _, _, _, err := s.LoadSnapshot(ctx, ghost); !errors.Is(err, store.ErrGameNotFound) {
		t.Fatalf("LoadSnapshot 未知对局应为 ErrGameNotFound，实际 %v", err)
	}
	if _, err := s.GetMeta(ctx, ghost); !errors.Is(err, store.ErrGameNotFound) {
		t.Fatalf("GetMeta 未知对局应为 ErrGameNotFound，实际 %v", err)
	}
}

// TestSnapshotLifecycle 验证快照「无则 ok=false、写后可读、重复写覆盖」。
func TestSnapshotLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	resetSchema(t, s.DB())
	if err := s.CreateGame(ctx, sampleMeta("snap-1")); err != nil {
		t.Fatalf("创建对局: %v", err)
	}

	if _, _, ok, err := s.LoadSnapshot(ctx, "snap-1"); err != nil || ok {
		t.Fatalf("初始应无快照: ok=%v err=%v", ok, err)
	}
	if err := s.SaveSnapshot(ctx, "snap-1", 10, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("保存快照: %v", err)
	}
	if body, at, ok, err := s.LoadSnapshot(ctx, "snap-1"); err != nil || !ok || at != 10 || string(body) != `{"v":1}` {
		t.Fatalf("读取快照失败: body=%s at=%d ok=%v err=%v", body, at, ok, err)
	}
	// 覆盖写：同一局只保留最新快照
	if err := s.SaveSnapshot(ctx, "snap-1", 42, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("覆盖快照: %v", err)
	}
	if body, at, ok, err := s.LoadSnapshot(ctx, "snap-1"); err != nil || !ok || at != 42 || string(body) != `{"v":2}` {
		t.Fatalf("覆盖后读取失败: body=%s at=%d ok=%v err=%v", body, at, ok, err)
	}
}

// TestConcurrentAppend 验证多个 goroutine 并发追加不同 seq 区间时互不干扰。
func TestConcurrentAppend(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	resetSchema(t, s.DB())
	if err := s.CreateGame(ctx, sampleMeta("concurrent-1")); err != nil {
		t.Fatalf("创建对局: %v", err)
	}

	const (
		writers     = 8
		perWriter   = 10
		totalEvents = writers * perWriter
	)
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			batch := make([]game.Event, 0, perWriter)
			for i := 0; i < perWriter; i++ {
				seq := uint64(writer*perWriter + i + 1)
				batch = append(batch, game.Event{Seq: seq, Kind: game.EventDraw, Seat: game.Seat(writer % 4), Tile: "1p"})
			}
			if err := s.AppendEvents(ctx, "concurrent-1", batch); err != nil {
				errCh <- err
			}
		}(writer)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发追加失败: %v", err)
	}

	got, err := s.LoadEvents(ctx, "concurrent-1", 0)
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	if len(got) != totalEvents {
		t.Fatalf("应有 %d 条，实际 %d", totalEvents, len(got))
	}
	for i, event := range got {
		if event.Seq != uint64(i+1) {
			t.Fatalf("事件未按 seq 升序：第 %d 条是 %d", i, event.Seq)
		}
	}
}

// TestOpenNormalizesDSN 验证 Open 会自动补 parseTime（否则 DATETIME 扫不进 time.Time）。
func TestOpenNormalizesDSN(t *testing.T) {
	ctx := context.Background()
	raw := mysqlDSN(t)
	// 去掉查询参数，构造一个「运维手写、没带 parseTime」的 DSN
	base := raw
	if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		base = raw[:idx]
	}

	s, err := sqlstore.Open(ctx, base)
	if err != nil {
		t.Fatalf("Open 未带 parseTime 的 DSN: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("建表: %v", err)
	}

	meta := sampleMeta("dsn-1")
	if err := s.CreateGame(ctx, meta); err != nil {
		t.Fatalf("创建对局: %v", err)
	}
	got, err := s.GetMeta(ctx, "dsn-1")
	if err != nil {
		t.Fatalf("GetMeta（说明时间列能扫成 time.Time）: %v", err)
	}
	if !got.CreatedAt.Equal(meta.CreatedAt) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at 往返失败: got=%v want=%v", got.CreatedAt, meta.CreatedAt)
	}
}
