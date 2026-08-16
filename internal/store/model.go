package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"oldwangvideo/internal/domain"
)

// CreateModel 手动新增模型及能力标注。
func (s *Store) CreateModel(ctx context.Context, input domain.ModelCreateInput) (domain.Model, error) {
	remoteID, name, capabilities, err := validateModel(input.RemoteID, input.Name, input.Capabilities)
	if err != nil {
		return domain.Model{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Model{}, fmt.Errorf("开始新增模型事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := nowText()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO models(provider_id, remote_id, name, manual, enabled, created_at, updated_at)
		VALUES(?, ?, ?, 1, ?, ?, ?)`, input.ProviderID, remoteID, name, boolInt(input.Enabled), now, now)
	if err != nil {
		return domain.Model{}, mapDatabaseError(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.Model{}, fmt.Errorf("读取模型编号: %w", err)
	}
	if err := replaceCapabilities(ctx, tx, id, capabilities); err != nil {
		return domain.Model{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Model{}, fmt.Errorf("提交新增模型事务: %w", err)
	}
	return s.GetModel(ctx, id)
}

// UpdateModel 更新模型名称、远端编号、能力和启用状态。
func (s *Store) UpdateModel(ctx context.Context, id int64, input domain.ModelUpdateInput) (domain.Model, error) {
	remoteID, name, capabilities, err := validateModel(input.RemoteID, input.Name, input.Capabilities)
	if err != nil {
		return domain.Model{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Model{}, fmt.Errorf("开始更新模型事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE models
		SET remote_id = ?, name = ?, enabled = ?, updated_at = ?
		WHERE id = ?`, remoteID, name, boolInt(input.Enabled), nowText(), id)
	if err != nil {
		return domain.Model{}, mapDatabaseError(err)
	}
	if err := requireAffected(result); err != nil {
		return domain.Model{}, err
	}
	if err := replaceCapabilities(ctx, tx, id, capabilities); err != nil {
		return domain.Model{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Model{}, fmt.Errorf("提交更新模型事务: %w", err)
	}
	return s.GetModel(ctx, id)
}

// SetModelEnabled 快速启用或禁用模型。
func (s *Store) SetModelEnabled(ctx context.Context, id int64, enabled bool) error {
	result, err := s.db.ExecContext(ctx, "UPDATE models SET enabled = ?, updated_at = ? WHERE id = ?", boolInt(enabled), nowText(), id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

// DeleteModel 删除模型，历史中的名称快照会保留。
func (s *Store) DeleteModel(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM models WHERE id = ?", id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

// GetModel 按编号读取模型及其能力。
func (s *Store) GetModel(ctx context.Context, id int64) (domain.Model, error) {
	rows, err := s.db.QueryContext(ctx, modelSelectSQL+" WHERE m.id = ? ORDER BY m.id, c.capability", id)
	if err != nil {
		return domain.Model{}, fmt.Errorf("查询模型: %w", err)
	}
	defer rows.Close()
	models, err := scanModels(rows)
	if err != nil {
		return domain.Model{}, err
	}
	if len(models) == 0 {
		return domain.Model{}, ErrNotFound
	}
	return models[0], nil
}

// ListModels 按供应商、能力和启用状态筛选模型。
func (s *Store) ListModels(ctx context.Context, filter domain.ModelFilter) ([]domain.Model, error) {
	if filter.Capability != "" && !filter.Capability.Valid() {
		return nil, fmt.Errorf("%w: 未知模型能力 %q", ErrInvalidInput, filter.Capability)
	}
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 2)
	if filter.ProviderID > 0 {
		conditions = append(conditions, "m.provider_id = ?")
		args = append(args, filter.ProviderID)
	}
	if filter.Capability != "" {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM model_capabilities selected
			WHERE selected.model_id = m.id AND selected.capability = ?
		)`)
		args = append(args, string(filter.Capability))
	}
	if filter.EnabledOnly || filter.UsableOnly {
		conditions = append(conditions, "m.enabled = 1")
	}
	if filter.UsableOnly {
		conditions = append(conditions, "p.enabled = 1")
	}
	query := modelSelectSQL
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY p.name COLLATE NOCASE, m.name COLLATE NOCASE, m.id, c.capability"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询模型列表: %w", err)
	}
	defer rows.Close()
	return scanModels(rows)
}

// UpsertModels 增量保存远端模型，不覆盖已有能力，也不修改手工模型。
func (s *Store) UpsertModels(ctx context.Context, providerID int64, remoteModels []domain.RemoteModel) ([]domain.Model, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开始同步模型事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var providerExists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(1) FROM providers WHERE id = ?", providerID).Scan(&providerExists); err != nil {
		return nil, fmt.Errorf("检查模型供应商: %w", err)
	}
	if providerExists == 0 {
		return nil, ErrNotFound
	}

	seen := make(map[string]struct{}, len(remoteModels))
	for _, remote := range remoteModels {
		remoteID, name, capabilities, err := validateModel(remote.ID, remote.Name, remote.Capabilities)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[remoteID]; exists {
			continue
		}
		seen[remoteID] = struct{}{}

		var id int64
		var manual int
		err = tx.QueryRowContext(ctx, `
			SELECT id, manual FROM models WHERE provider_id = ? AND remote_id = ?`, providerID, remoteID).Scan(&id, &manual)
		if err == nil {
			if manual == 0 {
				if _, err := tx.ExecContext(ctx, "UPDATE models SET name = ?, updated_at = ? WHERE id = ?", name, nowText(), id); err != nil {
					return nil, fmt.Errorf("更新远端模型 %q: %w", remoteID, err)
				}
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("检查远端模型 %q: %w", remoteID, err)
		}

		now := nowText()
		result, err := tx.ExecContext(ctx, `
			INSERT INTO models(provider_id, remote_id, name, manual, enabled, created_at, updated_at)
			VALUES(?, ?, ?, 0, 1, ?, ?)`, providerID, remoteID, name, now, now)
		if err != nil {
			return nil, mapDatabaseError(err)
		}
		id, err = result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("读取远端模型编号: %w", err)
		}
		if err := replaceCapabilities(ctx, tx, id, capabilities); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交同步模型事务: %w", err)
	}
	return s.ListModels(ctx, domain.ModelFilter{ProviderID: providerID})
}

// UpsertRemoteModels 是远端模型同步的语义化别名。
func (s *Store) UpsertRemoteModels(ctx context.Context, providerID int64, remoteModels []domain.RemoteModel) ([]domain.Model, error) {
	return s.UpsertModels(ctx, providerID, remoteModels)
}

const modelSelectSQL = `
	SELECT
		m.id, m.provider_id, p.name, m.remote_id, m.name, m.manual, m.enabled,
		m.created_at, m.updated_at, c.capability
	FROM models m
	JOIN providers p ON p.id = m.provider_id
	LEFT JOIN model_capabilities c ON c.model_id = m.id`

func scanModels(rows *sql.Rows) ([]domain.Model, error) {
	models := make([]domain.Model, 0)
	indexByID := make(map[int64]int)
	for rows.Next() {
		var model domain.Model
		var manual, enabled int
		var createdAt, updatedAt string
		var capability sql.NullString
		if err := rows.Scan(
			&model.ID,
			&model.ProviderID,
			&model.ProviderName,
			&model.RemoteID,
			&model.Name,
			&manual,
			&enabled,
			&createdAt,
			&updatedAt,
			&capability,
		); err != nil {
			return nil, fmt.Errorf("读取模型: %w", err)
		}
		position, exists := indexByID[model.ID]
		if !exists {
			var err error
			model.CreatedAt, err = parseTime(createdAt)
			if err != nil {
				return nil, err
			}
			model.UpdatedAt, err = parseTime(updatedAt)
			if err != nil {
				return nil, err
			}
			model.Manual = manual != 0
			model.Enabled = enabled != 0
			model.Capabilities = make([]domain.Capability, 0)
			models = append(models, model)
			position = len(models) - 1
			indexByID[model.ID] = position
		}
		if capability.Valid {
			models[position].Capabilities = append(models[position].Capabilities, domain.Capability(capability.String))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历模型: %w", err)
	}
	for i := range models {
		sortCapabilities(models[i].Capabilities)
	}
	return models, nil
}

func validateModel(remoteID, name string, capabilities []domain.Capability) (string, string, []domain.Capability, error) {
	remoteID = strings.TrimSpace(remoteID)
	name = strings.TrimSpace(name)
	if remoteID == "" {
		return "", "", nil, fmt.Errorf("%w: 模型编号不能为空", ErrInvalidInput)
	}
	if name == "" {
		name = remoteID
	}
	normalized, err := normalizeCapabilities(capabilities)
	if err != nil {
		return "", "", nil, err
	}
	return remoteID, name, normalized, nil
}

func normalizeCapabilities(capabilities []domain.Capability) ([]domain.Capability, error) {
	seen := make(map[domain.Capability]struct{}, len(capabilities))
	normalized := make([]domain.Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		if !capability.Valid() {
			return nil, fmt.Errorf("%w: 未知模型能力 %q", ErrInvalidInput, capability)
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		normalized = append(normalized, capability)
	}
	sortCapabilities(normalized)
	return normalized, nil
}

func sortCapabilities(capabilities []domain.Capability) {
	order := make(map[domain.Capability]int)
	for index, capability := range domain.AllCapabilities() {
		order[capability] = index
	}
	sort.SliceStable(capabilities, func(i, j int) bool {
		return order[capabilities[i]] < order[capabilities[j]]
	})
}

func replaceCapabilities(ctx context.Context, tx *sql.Tx, modelID int64, capabilities []domain.Capability) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM model_capabilities WHERE model_id = ?", modelID); err != nil {
		return fmt.Errorf("清理模型能力: %w", err)
	}
	for _, capability := range capabilities {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO model_capabilities(model_id, capability) VALUES(?, ?)`, modelID, string(capability)); err != nil {
			return fmt.Errorf("保存模型能力 %q: %w", capability, err)
		}
	}
	return nil
}
