package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"oldwangvideo/internal/domain"
)

// CreatePromptTemplate 新增提示词润色模板。
func (s *Store) CreatePromptTemplate(ctx context.Context, input domain.PromptTemplateInput) (domain.PromptTemplate, error) {
	name, systemPrompt, err := validatePromptTemplate(input)
	if err != nil {
		return domain.PromptTemplate{}, err
	}
	now := nowText()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO prompt_templates(name, system_prompt, enabled, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?)`, name, systemPrompt, boolInt(input.Enabled), now, now)
	if err != nil {
		return domain.PromptTemplate{}, mapDatabaseError(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.PromptTemplate{}, fmt.Errorf("读取模板编号: %w", err)
	}
	return s.GetPromptTemplate(ctx, id)
}

// UpdatePromptTemplate 更新提示词润色模板。
func (s *Store) UpdatePromptTemplate(ctx context.Context, id int64, input domain.PromptTemplateInput) (domain.PromptTemplate, error) {
	name, systemPrompt, err := validatePromptTemplate(input)
	if err != nil {
		return domain.PromptTemplate{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE prompt_templates
		SET name = ?, system_prompt = ?, enabled = ?, updated_at = ?
		WHERE id = ?`, name, systemPrompt, boolInt(input.Enabled), nowText(), id)
	if err != nil {
		return domain.PromptTemplate{}, mapDatabaseError(err)
	}
	if err := requireAffected(result); err != nil {
		return domain.PromptTemplate{}, err
	}
	return s.GetPromptTemplate(ctx, id)
}

// SetPromptTemplateEnabled 快速启用或禁用润色模板。
func (s *Store) SetPromptTemplateEnabled(ctx context.Context, id int64, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE prompt_templates SET enabled = ?, updated_at = ? WHERE id = ?`, boolInt(enabled), nowText(), id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

// GetPromptTemplate 按编号读取润色模板。
func (s *Store) GetPromptTemplate(ctx context.Context, id int64) (domain.PromptTemplate, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, system_prompt, enabled, created_at, updated_at
		FROM prompt_templates
		WHERE id = ?`, id)
	return scanPromptTemplate(row)
}

// ListPromptTemplates 按名称返回润色模板。
func (s *Store) ListPromptTemplates(ctx context.Context, includeDisabled bool) ([]domain.PromptTemplate, error) {
	query := `
		SELECT id, name, system_prompt, enabled, created_at, updated_at
		FROM prompt_templates`
	if !includeDisabled {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY name COLLATE NOCASE, id"
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询润色模板: %w", err)
	}
	defer rows.Close()
	templates := make([]domain.PromptTemplate, 0)
	for rows.Next() {
		template, err := scanPromptTemplate(rows)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历润色模板: %w", err)
	}
	return templates, nil
}

// DeletePromptTemplate 删除润色模板。
func (s *Store) DeletePromptTemplate(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM prompt_templates WHERE id = ?", id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

func validatePromptTemplate(input domain.PromptTemplateInput) (string, string, error) {
	name := strings.TrimSpace(input.Name)
	systemPrompt := strings.TrimSpace(input.SystemPrompt)
	if name == "" {
		return "", "", fmt.Errorf("%w: 模板名称不能为空", ErrInvalidInput)
	}
	if systemPrompt == "" {
		return "", "", fmt.Errorf("%w: 系统提示词不能为空", ErrInvalidInput)
	}
	return name, systemPrompt, nil
}

func scanPromptTemplate(scanner rowScanner) (domain.PromptTemplate, error) {
	var template domain.PromptTemplate
	var enabled int
	var createdAt, updatedAt string
	if err := scanner.Scan(
		&template.ID,
		&template.Name,
		&template.SystemPrompt,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return domain.PromptTemplate{}, mapDatabaseError(err)
	}
	var err error
	template.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.PromptTemplate{}, err
	}
	template.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return domain.PromptTemplate{}, err
	}
	template.Enabled = enabled != 0
	return template, nil
}

var _ rowScanner = (*sql.Row)(nil)
