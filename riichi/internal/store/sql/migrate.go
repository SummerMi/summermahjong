package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaStatements 是建表 DDL，Migrate 按顺序逐条执行。
//
// 统一 InnoDB + utf8mb4：utf8mb4 才能存四字节字符（emoji / 部分生僻字），
// InnoDB 才有事务与外键；DATETIME(6) 保留微秒，重放排序与审计更精确。
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS games (
		id          VARCHAR(64)     NOT NULL,
		seed        BIGINT          NOT NULL,
		rules_json  JSON            NOT NULL,
		agents_json JSON            NOT NULL,
		created_at  DATETIME(6)     NOT NULL,
		PRIMARY KEY (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='对局元数据'`,

	// 事件表是 append-only 日志：主键 (game_id, seq) 天然去重 + 按局按序范围扫描。
	// 外键 ON DELETE CASCADE 保证不会出现「孤儿事件」，也方便测试/运维整局清理。
	`CREATE TABLE IF NOT EXISTS game_events (
		game_id    VARCHAR(64)      NOT NULL,
		seq        BIGINT UNSIGNED  NOT NULL,
		kind       VARCHAR(32)      NOT NULL,
		seat       TINYINT UNSIGNED NOT NULL,
		payload    JSON             NULL,
		created_at DATETIME(6)      NOT NULL,
		PRIMARY KEY (game_id, seq),
		KEY idx_game_events_kind (game_id, kind),
		CONSTRAINT fk_game_events_game FOREIGN KEY (game_id)
			REFERENCES games (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='不可变事件日志'`,

	`CREATE TABLE IF NOT EXISTS snapshots (
		game_id    VARCHAR(64)      NOT NULL,
		at_seq     BIGINT UNSIGNED  NOT NULL,
		body       BLOB             NOT NULL,
		updated_at DATETIME(6)      NOT NULL,
		PRIMARY KEY (game_id),
		CONSTRAINT fk_snapshots_game FOREIGN KEY (game_id)
			REFERENCES games (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='状态快照'`,
}

// Migrate 幂等建表：应用启动时调用一次即可（CREATE TABLE IF NOT EXISTS）。
//
// 生产环境应换成版本化迁移（如 golang-migrate / Atlas）：IF NOT EXISTS 只能保证
// 「表存在」，不会把已有表升级到新列定义，也不会记录 schema 版本。
func Migrate(ctx context.Context, db *sql.DB) error {
	for _, statement := range schemaStatements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlstore: 迁移建表: %w", err)
		}
	}
	return nil
}

// Migrate 是 Migrate(ctx, s.DB()) 的便捷写法。
func (s *Store) Migrate(ctx context.Context) error {
	return Migrate(ctx, s.db)
}
