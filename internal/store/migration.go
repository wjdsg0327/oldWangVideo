package store

import (
	"context"
	"fmt"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		sql: `
			CREATE TABLE providers (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL CHECK (length(trim(name)) > 0),
				base_url TEXT NOT NULL CHECK (length(trim(base_url)) > 0),
				api_key_ciphertext TEXT NOT NULL DEFAULT '',
				enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);

			CREATE TABLE models (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
				remote_id TEXT NOT NULL CHECK (length(trim(remote_id)) > 0),
				name TEXT NOT NULL CHECK (length(trim(name)) > 0),
				manual INTEGER NOT NULL DEFAULT 0 CHECK (manual IN (0, 1)),
				enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				UNIQUE (provider_id, remote_id)
			);

			CREATE TABLE model_capabilities (
				model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
				capability TEXT NOT NULL CHECK (capability IN (
					'text_to_image', 'text_to_video', 'image_to_image', 'image_to_video', 'prompt_polish'
				)),
				PRIMARY KEY (model_id, capability)
			);

			CREATE TABLE prompt_templates (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(name)) > 0),
				system_prompt TEXT NOT NULL CHECK (length(trim(system_prompt)) > 0),
				enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);

			CREATE TABLE generation_history (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				task_id TEXT NOT NULL UNIQUE CHECK (length(trim(task_id)) > 0),
				batch_id TEXT NOT NULL DEFAULT '',
				provider_id INTEGER REFERENCES providers(id) ON DELETE SET NULL,
				model_id INTEGER REFERENCES models(id) ON DELETE SET NULL,
				provider_name TEXT NOT NULL,
				model_name TEXT NOT NULL,
				capability TEXT NOT NULL CHECK (capability IN (
					'text_to_image', 'text_to_video', 'image_to_image', 'image_to_video'
				)),
				request_json TEXT NOT NULL,
				status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'canceled')),
				result_json TEXT,
				error_message TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				started_at TEXT,
				completed_at TEXT,
				updated_at TEXT NOT NULL
			);

			CREATE TABLE favorites (
				history_id INTEGER PRIMARY KEY REFERENCES generation_history(id) ON DELETE CASCADE,
				created_at TEXT NOT NULL
			);

			CREATE INDEX idx_models_provider ON models(provider_id, enabled, name);
			CREATE INDEX idx_model_capabilities_capability ON model_capabilities(capability, model_id);
			CREATE INDEX idx_history_created ON generation_history(created_at DESC, id DESC);
			CREATE INDEX idx_history_status ON generation_history(status, created_at DESC);
			CREATE INDEX idx_history_capability ON generation_history(capability, created_at DESC);
			CREATE INDEX idx_favorites_created ON favorites(created_at DESC);
		`,
	},
	{
		version: 2,
		sql: `
			ALTER TABLE providers ADD COLUMN proxy_url TEXT NOT NULL DEFAULT '';
		`,
	},
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("创建迁移表: %w", err)
	}

	for _, item := range migrations {
		if err := s.applyMigration(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, item migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始数据库迁移 %d: %w", item.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(1) FROM schema_migrations WHERE version = ?", item.version).Scan(&exists); err != nil {
		return fmt.Errorf("检查数据库迁移 %d: %w", item.version, err)
	}
	if exists > 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, item.sql); err != nil {
		return fmt.Errorf("执行数据库迁移 %d: %w", item.version, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)", item.version, nowText()); err != nil {
		return fmt.Errorf("记录数据库迁移 %d: %w", item.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交数据库迁移 %d: %w", item.version, err)
	}
	return nil
}
