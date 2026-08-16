package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"oldwangvideo/internal/domain"
)

// CreateGenerationHistory 创建一条独立模型任务的生成历史。
func (s *Store) CreateGenerationHistory(ctx context.Context, input domain.HistoryCreateInput) (domain.GenerationHistory, error) {
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.BatchID = strings.TrimSpace(input.BatchID)
	if input.TaskID == "" {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 任务编号不能为空", ErrInvalidInput)
	}
	if input.ProviderID <= 0 || input.ModelID <= 0 {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 供应商和模型编号无效", ErrInvalidInput)
	}
	if !input.Capability.IsGeneration() {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 生成能力无效", ErrInvalidInput)
	}
	if input.Status == "" {
		input.Status = domain.TaskStatusQueued
	}
	if !input.Status.Valid() {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 任务状态无效", ErrInvalidInput)
	}
	if input.Request.Capability == "" {
		input.Request.Capability = input.Capability
	}
	if input.Request.Capability != input.Capability {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 请求能力与历史能力不一致", ErrInvalidInput)
	}
	providerName, modelName, err := s.resolveHistoryNames(ctx, input.ProviderID, input.ModelID)
	if err != nil {
		return domain.GenerationHistory{}, err
	}
	if strings.TrimSpace(input.ProviderName) != "" {
		providerName = strings.TrimSpace(input.ProviderName)
	}
	if strings.TrimSpace(input.ModelName) != "" {
		modelName = strings.TrimSpace(input.ModelName)
	}
	requestJSON, err := json.Marshal(input.Request)
	if err != nil {
		return domain.GenerationHistory{}, fmt.Errorf("序列化生成请求: %w", err)
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var startedAt, completedAt any
	if input.Status == domain.TaskStatusRunning {
		startedAt = timeText(createdAt)
	}
	if input.Status.Terminal() {
		completedAt = timeText(createdAt)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO generation_history(
			task_id, batch_id, provider_id, model_id, provider_name, model_name,
			capability, request_json, status, created_at, started_at, completed_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.TaskID,
		input.BatchID,
		input.ProviderID,
		input.ModelID,
		providerName,
		modelName,
		string(input.Capability),
		string(requestJSON),
		string(input.Status),
		timeText(createdAt),
		startedAt,
		completedAt,
		timeText(createdAt),
	)
	if err != nil {
		return domain.GenerationHistory{}, mapDatabaseError(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.GenerationHistory{}, fmt.Errorf("读取生成历史编号: %w", err)
	}
	return s.GetHistory(ctx, id)
}

// CreateGenerationRecord 是创建生成历史的服务层友好别名。
func (s *Store) CreateGenerationRecord(ctx context.Context, input domain.HistoryCreateInput) (domain.GenerationHistory, error) {
	return s.CreateGenerationHistory(ctx, input)
}

// UpdateGenerationHistory 按任务编号更新状态、结果和时间。
func (s *Store) UpdateGenerationHistory(ctx context.Context, taskID string, update domain.HistoryStatusUpdate) (domain.GenerationHistory, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 任务编号不能为空", ErrInvalidInput)
	}
	if !update.Status.Valid() {
		return domain.GenerationHistory{}, fmt.Errorf("%w: 任务状态无效", ErrInvalidInput)
	}
	fields := []string{"status = ?", "updated_at = ?"}
	args := []any{string(update.Status), nowText()}
	if update.Result != nil {
		resultJSON, err := json.Marshal(update.Result)
		if err != nil {
			return domain.GenerationHistory{}, fmt.Errorf("序列化生成结果: %w", err)
		}
		fields = append(fields, "result_json = ?")
		args = append(args, string(resultJSON))
	}
	if update.ErrorMessage != nil {
		fields = append(fields, "error_message = ?")
		args = append(args, strings.TrimSpace(*update.ErrorMessage))
	}
	if update.StartedAt != nil {
		fields = append(fields, "started_at = ?")
		args = append(args, timeText(*update.StartedAt))
	} else if update.Status == domain.TaskStatusRunning {
		now := time.Now().UTC()
		fields = append(fields, "started_at = COALESCE(started_at, ?)")
		args = append(args, timeText(now))
	}
	if update.CompletedAt != nil {
		fields = append(fields, "completed_at = ?")
		args = append(args, timeText(*update.CompletedAt))
	} else if update.Status.Terminal() {
		now := time.Now().UTC()
		fields = append(fields, "completed_at = COALESCE(completed_at, ?)")
		args = append(args, timeText(now))
	}
	args = append(args, taskID)
	result, err := s.db.ExecContext(ctx, `
		UPDATE generation_history SET `+strings.Join(fields, ", ")+` WHERE task_id = ?`, args...)
	if err != nil {
		return domain.GenerationHistory{}, mapDatabaseError(err)
	}
	if err := requireAffected(result); err != nil {
		return domain.GenerationHistory{}, err
	}
	return s.GetHistoryByTaskID(ctx, taskID)
}

// UpdateGenerationRecord 是更新生成历史的服务层友好别名。
func (s *Store) UpdateGenerationRecord(ctx context.Context, taskID string, update domain.HistoryStatusUpdate) (domain.GenerationHistory, error) {
	return s.UpdateGenerationHistory(ctx, taskID, update)
}

// GetHistory 按数据库编号读取生成历史。
func (s *Store) GetHistory(ctx context.Context, id int64) (domain.GenerationHistory, error) {
	row := s.db.QueryRowContext(ctx, historySelectSQL+" WHERE h.id = ?", id)
	return scanHistory(row)
}

// GetHistoryByTaskID 按任务编号读取生成历史。
func (s *Store) GetHistoryByTaskID(ctx context.Context, taskID string) (domain.GenerationHistory, error) {
	row := s.db.QueryRowContext(ctx, historySelectSQL+" WHERE h.task_id = ?", strings.TrimSpace(taskID))
	return scanHistory(row)
}

// ListHistory 按能力、状态、收藏和关键词分页查询历史。
func (s *Store) ListHistory(ctx context.Context, filter domain.HistoryFilter) (domain.HistoryPage, error) {
	if filter.Capability != "" && !filter.Capability.IsGeneration() {
		return domain.HistoryPage{}, fmt.Errorf("%w: 历史能力无效", ErrInvalidInput)
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return domain.HistoryPage{}, fmt.Errorf("%w: 历史状态无效", ErrInvalidInput)
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if filter.Capability != "" {
		conditions = append(conditions, "h.capability = ?")
		args = append(args, string(filter.Capability))
	}
	if filter.Status != "" {
		conditions = append(conditions, "h.status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.FavoriteOnly {
		conditions = append(conditions, "f.history_id IS NOT NULL")
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		conditions = append(conditions, `(
			h.provider_name LIKE ? ESCAPE '\' OR
			h.model_name LIKE ? ESCAPE '\' OR
			h.request_json LIKE ? ESCAPE '\'
		)`)
		pattern := "%" + escapeLike(search) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM generation_history h
		LEFT JOIN favorites f ON f.history_id = h.id`+where, args...).Scan(&total); err != nil {
		return domain.HistoryPage{}, fmt.Errorf("统计生成历史: %w", err)
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, historySelectSQL+where+`
		ORDER BY h.created_at DESC, h.id DESC
		LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return domain.HistoryPage{}, fmt.Errorf("查询生成历史: %w", err)
	}
	defer rows.Close()
	items := make([]domain.GenerationHistory, 0)
	for rows.Next() {
		item, err := scanHistory(rows)
		if err != nil {
			return domain.HistoryPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.HistoryPage{}, fmt.Errorf("遍历生成历史: %w", err)
	}
	return domain.HistoryPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// ListFavorites 分页返回收藏的生成历史。
func (s *Store) ListFavorites(ctx context.Context, page domain.PageRequest) (domain.HistoryPage, error) {
	return s.ListHistory(ctx, domain.HistoryFilter{
		PageRequest:  page,
		FavoriteOnly: true,
	})
}

// ListFavoriteRecords 返回显式包含收藏时间的分页结构。
func (s *Store) ListFavoriteRecords(ctx context.Context, page domain.PageRequest) (domain.FavoritePage, error) {
	historyPage, err := s.ListFavorites(ctx, page)
	if err != nil {
		return domain.FavoritePage{}, err
	}
	items := make([]domain.Favorite, 0, len(historyPage.Items))
	for _, history := range historyPage.Items {
		createdAt := history.UpdatedAt
		if history.FavoritedAt != nil {
			createdAt = *history.FavoritedAt
		}
		items = append(items, domain.Favorite{History: history, CreatedAt: createdAt})
	}
	return domain.FavoritePage{
		Items:    items,
		Total:    historyPage.Total,
		Page:     historyPage.Page,
		PageSize: historyPage.PageSize,
	}, nil
}

// SetFavorite 幂等设置一条生成历史的收藏状态。
func (s *Store) SetFavorite(ctx context.Context, historyID int64, favorite bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始收藏事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(1) FROM generation_history WHERE id = ?", historyID).Scan(&exists); err != nil {
		return fmt.Errorf("检查生成历史: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	if favorite {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO favorites(history_id, created_at) VALUES(?, ?)
			ON CONFLICT(history_id) DO NOTHING`, historyID, nowText()); err != nil {
			return fmt.Errorf("收藏生成历史: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, "DELETE FROM favorites WHERE history_id = ?", historyID); err != nil {
		return fmt.Errorf("取消收藏生成历史: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交收藏事务: %w", err)
	}
	return nil
}

// DeleteHistory 删除生成历史及其收藏关系。
func (s *Store) DeleteHistory(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM generation_history WHERE id = ?", id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

// DeleteHistories 在一个事务中删除多条生成历史及其收藏关系。
func (s *Store) DeleteHistories(ctx context.Context, ids []int64) error {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("%w: 历史记录编号无效", ErrInvalidInput)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return fmt.Errorf("%w: 没有要删除的历史记录", ErrInvalidInput)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始批量删除事务: %w", err)
	}
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for index, id := range unique {
		placeholders[index] = "?"
		args[index] = id
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM generation_history WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		_ = tx.Rollback()
		return mapDatabaseError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("读取批量删除结果: %w", err)
	}
	if affected != int64(len(unique)) {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交批量删除事务: %w", err)
	}
	return nil
}

const historySelectSQL = `
	SELECT
		h.id, h.task_id, h.batch_id, h.provider_id, h.model_id,
		h.provider_name, h.model_name, h.capability, h.request_json,
		h.status, h.result_json, h.error_message,
		h.created_at, h.started_at, h.completed_at, h.updated_at,
		f.created_at
	FROM generation_history h
	LEFT JOIN favorites f ON f.history_id = h.id`

func scanHistory(scanner rowScanner) (domain.GenerationHistory, error) {
	var history domain.GenerationHistory
	var providerID, modelID sql.NullInt64
	var requestJSON string
	var resultJSON, startedAt, completedAt, favoritedAt sql.NullString
	var createdAt, updatedAt string
	if err := scanner.Scan(
		&history.ID,
		&history.TaskID,
		&history.BatchID,
		&providerID,
		&modelID,
		&history.ProviderName,
		&history.ModelName,
		&history.Capability,
		&requestJSON,
		&history.Status,
		&resultJSON,
		&history.ErrorMessage,
		&createdAt,
		&startedAt,
		&completedAt,
		&updatedAt,
		&favoritedAt,
	); err != nil {
		return domain.GenerationHistory{}, mapDatabaseError(err)
	}
	if providerID.Valid {
		history.ProviderID = providerID.Int64
	}
	if modelID.Valid {
		history.ModelID = modelID.Int64
	}
	if err := json.Unmarshal([]byte(requestJSON), &history.Request); err != nil {
		return domain.GenerationHistory{}, fmt.Errorf("解析历史请求: %w", err)
	}
	if history.Request.Images == nil {
		history.Request.Images = make([]domain.ImageInput, 0)
	}
	if history.Request.Targets == nil {
		history.Request.Targets = make([]domain.ModelTarget, 0)
	}
	if resultJSON.Valid && resultJSON.String != "" {
		var result domain.GenerationResult
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return domain.GenerationHistory{}, fmt.Errorf("解析历史结果: %w", err)
		}
		if result.Assets == nil {
			result.Assets = make([]domain.MediaAsset, 0)
		}
		history.Result = &result
	}
	var err error
	history.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.GenerationHistory{}, err
	}
	history.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return domain.GenerationHistory{}, err
	}
	history.StartedAt, err = parseNullableTime(startedAt)
	if err != nil {
		return domain.GenerationHistory{}, err
	}
	history.CompletedAt, err = parseNullableTime(completedAt)
	if err != nil {
		return domain.GenerationHistory{}, err
	}
	history.FavoritedAt, err = parseNullableTime(favoritedAt)
	if err != nil {
		return domain.GenerationHistory{}, err
	}
	history.Favorite = history.FavoritedAt != nil
	return history, nil
}

func (s *Store) resolveHistoryNames(ctx context.Context, providerID, modelID int64) (string, string, error) {
	var providerName, modelName string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.name, m.name
		FROM models m
		JOIN providers p ON p.id = m.provider_id
		WHERE p.id = ? AND m.id = ?`, providerID, modelID).Scan(&providerName, &modelName)
	if err != nil {
		return "", "", mapDatabaseError(err)
	}
	return providerName, modelName, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}
