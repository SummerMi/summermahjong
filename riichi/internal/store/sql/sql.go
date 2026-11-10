// Package sqlstore 是 store.Store 的 MySQL 实现（契约 S7）。
//
// 依赖方向遵循契约 §1：sqlstore → store → game，不反向依赖 transport。
//
// 表结构（三张表，均可由 Migrate 幂等创建）：
//
//	games       一局对局的元数据（id 主键、seed、rules_json、agents_json、created_at）
//	game_events 不可变事件日志（PK(game_id, seq)，payload 存事件体 JSON）
//	snapshots   某一 seq 处的状态快照（game_id 主键，body 为调用方序列化后的字节）
//
// 一致性约定（与 store.Memory 对齐，由 storetest 套件验证）：
//   - CreateGame 幂等：同 id 重复创建不覆盖既有元数据、不报错；
//   - AppendEvents 单调追加且幂等：重复 seq 静默保留首次写入（ON DUPLICATE KEY UPDATE 空操作），
//     整批在一个事务里完成；对不存在的对局返回 store.ErrGameNotFound；
//   - LoadEvents 按 seq 升序返回 seq > fromSeq 的事件；
//   - SaveSnapshot 覆盖写；LoadSnapshot 无快照时 ok=false。
//
// M1 的 game.Event 除契约字段（Seq/Kind/Seat/Payload）外还有 Tile/TileRed 两个便于断言的字段，
// 而表结构只给了 payload 一列 JSON：这里把「事件体」编码成 JSON 对象存进 payload 列
// （payload 键内嵌原始 Payload），从而保证 LoadEvents 能逐字段无损还原、回放不丢信息。
//
// 注意：MySQL 的 JSON 列会归一化 JSON（去掉空白、按内部规则重排对象 key），
// 因此 LoadEvents 读回的 Payload 与写入时的**字节**不保证相同，但**语义**相同。
// 回放/比对事件时应按 JSON 语义比较（反序列化后比较），不要用字节相等。
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

// maxEventsPerBatch 单条 INSERT 最多携带的事件数。
//
// 批次上限是为了避免一次写入触发 max_allowed_packet / 超长占位符列表；
// 500 条 × 6 列 = 3000 个占位符，远小于 MySQL 默认 65535 的限制。
const maxEventsPerBatch = 500

// eventBody 是 game_events.payload 列里存放的事件体。
//
// 表结构把事件内容统一收敛到 payload 一列，而 M1 的 Event 还有 Tile/TileRed，
// 所以这里用一层显式包装：既有载荷原样嵌在 payload 键里，也保留了 M1 的牌面字段。
type eventBody struct {
	Tile    string          `json:"tile,omitempty"`
	TileRed bool            `json:"tile_red,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Store 是 store.Store 的 MySQL 实现。
//
// 所有方法都通过 database/sql 的连接池并发安全执行，Store 自身无可变状态，
// 可以在同一个 *sql.DB 上构造多个实例（每个实例等价于「一个进程的视图」）。
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New 用已有连接池构造 Store；调用方负责最终 Close 连接池。
func New(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now}
}

// Open 按 DSN 建连接池、ping 一次并构造 Store。
//
// 会自动把 parseTime=true、loc=UTC 补进 DSN：MySQL 的 DATETIME 不带时区，
// 不开 parseTime 时驱动会把时间扫成 []byte，GetMeta 就没法还原 created_at。
// 返回的 Store.Close 会关闭自己建的连接池。
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := mysqlDriver.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: 解析 DSN: %w", err)
	}
	cfg.ParseTime = true
	if cfg.Loc == nil {
		cfg.Loc = time.UTC
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("sqlstore: 打开数据库: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlstore: 连接数据库: %w", err)
	}
	return New(db), nil
}

// DB 暴露底层连接池（供 Migrate / 健康检查 / 测试使用）。
func (s *Store) DB() *sql.DB { return s.db }

// Close 关闭底层连接池。只应由创建者调用一次。
func (s *Store) Close() error { return s.db.Close() }

// CreateGame 实现 store.Store：写入元数据，已存在时保持原值（幂等）。
func (s *Store) CreateGame(ctx context.Context, meta store.GameMeta) error {
	rulesJSON, err := json.Marshal(meta.Rules)
	if err != nil {
		return fmt.Errorf("sqlstore: 序列化 rules: %w", err)
	}
	agentsJSON, err := json.Marshal(meta.Agents)
	if err != nil {
		return fmt.Errorf("sqlstore: 序列化 agents: %w", err)
	}
	createdAt := meta.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now().UTC()
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO games (id, seed, rules_json, agents_json, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE id = id`,
		meta.ID, meta.Seed, rulesJSON, agentsJSON, createdAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("sqlstore: 创建对局: %w", err)
	}
	return nil
}

// AppendEvents 实现 store.Store：在一个事务里批量追加事件，重复 seq 幂等。
//
// 事件会先按 Seq 升序排序再写：多行 INSERT 在 InnoDB 上按主键顺序加锁，
// 统一顺序可以降低并发追加之间的死锁概率。
func (s *Store) AppendEvents(ctx context.Context, gameID string, events []game.Event) error {
	if len(events) == 0 {
		// 与 Memory 一致：空批次也要校验对局存在。
		return s.ensureGame(ctx, gameID)
	}

	ordered := make([]game.Event, len(events))
	copy(ordered, events)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: 开始事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // Commit 成功后 Rollback 是空操作

	if err := ensureGameTx(ctx, tx, gameID); err != nil {
		return err
	}

	now := s.now().UTC()
	for start := 0; start < len(ordered); start += maxEventsPerBatch {
		end := min(start+maxEventsPerBatch, len(ordered))
		if err := insertEventsBatch(ctx, tx, gameID, ordered[start:end], now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: 提交事件: %w", err)
	}
	return nil
}

// LoadEvents 实现 store.Store：返回 seq 严格大于 fromSeq 的事件，按 seq 升序。
func (s *Store) LoadEvents(ctx context.Context, gameID string, fromSeq uint64) ([]game.Event, error) {
	if err := s.ensureGame(ctx, gameID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, kind, seat, payload
		   FROM game_events
		  WHERE game_id = ? AND seq > ?
		  ORDER BY seq ASC`,
		gameID, fromSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: 查询事件: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]game.Event, 0, 16)
	for rows.Next() {
		var (
			seq     uint64
			kind    string
			seat    uint8
			payload []byte
		)
		if err := rows.Scan(&seq, &kind, &seat, &payload); err != nil {
			return nil, fmt.Errorf("sqlstore: 扫描事件: %w", err)
		}
		event := game.Event{Seq: seq, Kind: game.EventKind(kind), Seat: game.Seat(seat)}
		if len(payload) > 0 {
			var body eventBody
			if err := json.Unmarshal(payload, &body); err != nil {
				return nil, fmt.Errorf("sqlstore: 解析事件体 seq=%d: %w", seq, err)
			}
			event.Tile = body.Tile
			event.TileRed = body.TileRed
			event.Payload = body.Payload
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: 遍历事件: %w", err)
	}
	return out, nil
}

// SaveSnapshot 实现 store.Store：覆盖保存某 seq 处的快照。
func (s *Store) SaveSnapshot(ctx context.Context, gameID string, atSeq uint64, snapshot []byte) error {
	if err := s.ensureGame(ctx, gameID); err != nil {
		return err
	}
	if snapshot == nil {
		snapshot = []byte{}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO snapshots (game_id, at_seq, body, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE at_seq = VALUES(at_seq), body = VALUES(body), updated_at = VALUES(updated_at)`,
		gameID, atSeq, snapshot, s.now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("sqlstore: 保存快照: %w", err)
	}
	return nil
}

// LoadSnapshot 实现 store.Store：没有快照时返回 ok=false。
func (s *Store) LoadSnapshot(ctx context.Context, gameID string) ([]byte, uint64, bool, error) {
	var (
		atSeq uint64
		body  []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT at_seq, body FROM snapshots WHERE game_id = ?`, gameID,
	).Scan(&atSeq, &body)
	if errors.Is(err, sql.ErrNoRows) {
		// 区分「对局不存在」与「对局存在但还没快照」：前者与 Memory 一样报 ErrGameNotFound。
		if err := s.ensureGame(ctx, gameID); err != nil {
			return nil, 0, false, err
		}
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("sqlstore: 读取快照: %w", err)
	}
	// 拷贝一份，避免调用方持有驱动复用的缓冲区。
	out := make([]byte, len(body))
	copy(out, body)
	return out, atSeq, true, nil
}

// GetMeta 实现 store.Store：读取对局元数据。
func (s *Store) GetMeta(ctx context.Context, gameID string) (store.GameMeta, error) {
	var (
		meta       store.GameMeta
		seed       int64
		rulesJSON  []byte
		agentsJSON []byte
		createdAt  time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, seed, rules_json, agents_json, created_at FROM games WHERE id = ?`, gameID,
	).Scan(&meta.ID, &seed, &rulesJSON, &agentsJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.GameMeta{}, store.ErrGameNotFound
	}
	if err != nil {
		return store.GameMeta{}, fmt.Errorf("sqlstore: 读取元数据: %w", err)
	}
	if err := json.Unmarshal(rulesJSON, &meta.Rules); err != nil {
		return store.GameMeta{}, fmt.Errorf("sqlstore: 解析 rules_json: %w", err)
	}
	if err := json.Unmarshal(agentsJSON, &meta.Agents); err != nil {
		return store.GameMeta{}, fmt.Errorf("sqlstore: 解析 agents_json: %w", err)
	}
	meta.Seed = seed
	meta.CreatedAt = createdAt.UTC()
	return meta, nil
}

// ensureGame 校验对局存在，不存在返回 store.ErrGameNotFound。
func (s *Store) ensureGame(ctx context.Context, gameID string) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM games WHERE id = ?`, gameID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrGameNotFound
	}
	if err != nil {
		return fmt.Errorf("sqlstore: 校验对局: %w", err)
	}
	return nil
}

// ensureGameTx 是事务内的 ensureGame：在追加事件前统一校验，保证错误码一致。
func ensureGameTx(ctx context.Context, tx *sql.Tx, gameID string) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM games WHERE id = ?`, gameID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrGameNotFound
	}
	if err != nil {
		return fmt.Errorf("sqlstore: 校验对局: %w", err)
	}
	return nil
}

// insertEventsBatch 拼一条多行 INSERT 写入一批事件。
//
// ON DUPLICATE KEY UPDATE game_id = game_id 是空操作：已存在的 (game_id, seq) 保持首次写入的内容，
// 因此重复追加同一批事件是幂等的；同一批里出现重复 seq 时同样是「先出现的那条生效」。
func insertEventsBatch(ctx context.Context, tx *sql.Tx, gameID string, events []game.Event, now time.Time) error {
	var builder []byte
	builder = append(builder, `INSERT INTO game_events (game_id, seq, kind, seat, payload, created_at) VALUES `...)
	args := make([]any, 0, len(events)*6)
	for i, event := range events {
		body, err := json.Marshal(eventBody{
			Tile:    event.Tile,
			TileRed: event.TileRed,
			Payload: event.Payload,
		})
		if err != nil {
			return fmt.Errorf("sqlstore: 序列化事件 seq=%d: %w", event.Seq, err)
		}
		if i > 0 {
			builder = append(builder, ',')
		}
		builder = append(builder, "(?,?,?,?,?,?)"...)
		args = append(args, gameID, event.Seq, string(event.Kind), uint8(event.Seat), body, now)
	}
	builder = append(builder, ` ON DUPLICATE KEY UPDATE game_id = game_id`...)
	if _, err := tx.ExecContext(ctx, string(builder), args...); err != nil {
		return fmt.Errorf("sqlstore: 写入事件: %w", err)
	}
	return nil
}
