package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"oldwangvideo/internal/domain"
)

// CreateProvider 加密 API Key 后新增供应商。
func (s *Store) CreateProvider(ctx context.Context, input domain.ProviderCreateInput) (domain.Provider, error) {
	name, baseURL, err := validateProvider(input.Name, input.BaseURL)
	if err != nil {
		return domain.Provider{}, err
	}
	if err := validateKeyTransport(baseURL, strings.TrimSpace(input.APIKey) != ""); err != nil {
		return domain.Provider{}, err
	}
	ciphertext, err := s.vault.Encrypt(strings.TrimSpace(input.APIKey))
	if err != nil {
		return domain.Provider{}, fmt.Errorf("加密供应商密钥: %w", err)
	}
	now := nowText()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO providers(name, base_url, api_key_ciphertext, enabled, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)`, name, baseURL, ciphertext, boolInt(input.Enabled), now, now)
	if err != nil {
		return domain.Provider{}, mapDatabaseError(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.Provider{}, fmt.Errorf("读取供应商编号: %w", err)
	}
	return s.GetProvider(ctx, id)
}

// UpdateProvider 更新供应商，API Key 未传入时不会改写密文。
func (s *Store) UpdateProvider(ctx context.Context, id int64, input domain.ProviderUpdateInput) (domain.Provider, error) {
	name, baseURL, err := validateProvider(input.Name, input.BaseURL)
	if err != nil {
		return domain.Provider{}, err
	}
	hasAPIKey := input.APIKey != nil && strings.TrimSpace(*input.APIKey) != ""
	if input.APIKey == nil {
		current, getErr := s.GetProvider(ctx, id)
		if getErr != nil {
			return domain.Provider{}, getErr
		}
		hasAPIKey = current.HasAPIKey
	}
	if err := validateKeyTransport(baseURL, hasAPIKey); err != nil {
		return domain.Provider{}, err
	}
	var result sql.Result
	if input.APIKey == nil {
		result, err = s.db.ExecContext(ctx, `
			UPDATE providers
			SET name = ?, base_url = ?, enabled = ?, updated_at = ?
			WHERE id = ?`, name, baseURL, boolInt(input.Enabled), nowText(), id)
	} else {
		ciphertext, encryptErr := s.vault.Encrypt(strings.TrimSpace(*input.APIKey))
		if encryptErr != nil {
			return domain.Provider{}, fmt.Errorf("加密供应商密钥: %w", encryptErr)
		}
		result, err = s.db.ExecContext(ctx, `
			UPDATE providers
			SET name = ?, base_url = ?, api_key_ciphertext = ?, enabled = ?, updated_at = ?
			WHERE id = ?`, name, baseURL, ciphertext, boolInt(input.Enabled), nowText(), id)
	}
	if err != nil {
		return domain.Provider{}, mapDatabaseError(err)
	}
	if err := requireAffected(result); err != nil {
		return domain.Provider{}, err
	}
	return s.GetProvider(ctx, id)
}

// SetProviderEnabled 快速启用或禁用供应商。
func (s *Store) SetProviderEnabled(ctx context.Context, id int64, enabled bool) error {
	result, err := s.db.ExecContext(ctx, "UPDATE providers SET enabled = ?, updated_at = ? WHERE id = ?", boolInt(enabled), nowText(), id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

// GetProvider 按编号读取不含完整密钥的供应商。
func (s *Store) GetProvider(ctx context.Context, id int64) (domain.Provider, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, base_url, api_key_ciphertext, enabled, created_at, updated_at
		FROM providers
		WHERE id = ?`, id)
	provider, _, err := scanProviderWithCiphertext(row)
	return provider, err
}

// ListProviders 按名称返回供应商列表。
func (s *Store) ListProviders(ctx context.Context, includeDisabled bool) ([]domain.Provider, error) {
	query := `
		SELECT id, name, base_url, api_key_ciphertext, enabled, created_at, updated_at
		FROM providers`
	if !includeDisabled {
		query += " WHERE enabled = 1"
	}
	query += " ORDER BY name COLLATE NOCASE, id"

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询供应商: %w", err)
	}
	defer rows.Close()
	providers := make([]domain.Provider, 0)
	for rows.Next() {
		provider, _, err := scanProviderWithCiphertext(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历供应商: %w", err)
	}
	return providers, nil
}

// DeleteProvider 删除供应商及其模型，历史中的名称快照会保留。
func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM providers WHERE id = ?", id)
	if err != nil {
		return mapDatabaseError(err)
	}
	return requireAffected(result)
}

func validateProvider(name, baseURL string) (string, string, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if name == "" {
		return "", "", fmt.Errorf("%w: 供应商名称不能为空", ErrInvalidInput)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", fmt.Errorf("%w: Base URL 必须是 http 或 https 地址", ErrInvalidInput)
	}
	return name, baseURL, nil
}

func validateKeyTransport(baseURL string, hasAPIKey bool) error {
	if !hasAPIKey {
		return nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" {
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "127.") {
		return nil
	}
	return fmt.Errorf("%w: 携带 API Key 的远程供应商必须使用 HTTPS（本机回环地址可使用 HTTP）", ErrInvalidInput)
}

func scanProviderWithCiphertext(scanner rowScanner) (domain.Provider, string, error) {
	var provider domain.Provider
	var ciphertext, createdAt, updatedAt string
	var enabled int
	if err := scanner.Scan(
		&provider.ID,
		&provider.Name,
		&provider.BaseURL,
		&ciphertext,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return domain.Provider{}, "", mapDatabaseError(err)
	}
	var err error
	provider.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return domain.Provider{}, "", err
	}
	provider.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return domain.Provider{}, "", err
	}
	provider.Enabled = enabled != 0
	provider.HasAPIKey = ciphertext != ""
	return provider, ciphertext, nil
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取更新结果: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
