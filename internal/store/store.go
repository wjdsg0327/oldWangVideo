package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oldwangvideo/internal/domain"
	"oldwangvideo/internal/security"

	_ "modernc.org/sqlite"
)

const defaultDatabaseName = "oldwangvideo.db"

var (
	ErrNotFound     = errors.New("记录不存在")
	ErrConflict     = errors.New("记录冲突")
	ErrInvalidInput = errors.New("输入无效")
)

// Options 控制本地数据库和连接池参数。
type Options struct {
	DataDir      string
	DatabaseName string
	BusyTimeout  time.Duration
	MaxOpenConns int
	MaxIdleConns int
}

// Store 封装 SQLite 数据库和本地密钥保管器。
type Store struct {
	db    *sql.DB
	vault *security.Vault
}

// Open 使用默认数据库名打开数据目录。
func Open(ctx context.Context, dataDir string) (*Store, error) {
	return OpenWithOptions(ctx, Options{DataDir: dataDir})
}

// OpenWithOptions 初始化 SQLite、连接参数和全部迁移。
func OpenWithOptions(ctx context.Context, options Options) (*Store, error) {
	options.DataDir = strings.TrimSpace(options.DataDir)
	if options.DataDir == "" {
		return nil, fmt.Errorf("%w: 数据目录不能为空", ErrInvalidInput)
	}
	absoluteDataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return nil, fmt.Errorf("解析数据目录: %w", err)
	}
	options.DataDir = absoluteDataDir
	if options.DatabaseName == "" {
		options.DatabaseName = defaultDatabaseName
	}
	if filepath.Base(options.DatabaseName) != options.DatabaseName {
		return nil, fmt.Errorf("%w: 数据库名称不能包含路径", ErrInvalidInput)
	}
	if options.BusyTimeout <= 0 {
		options.BusyTimeout = 5 * time.Second
	}
	if options.MaxOpenConns <= 0 {
		options.MaxOpenConns = 8
	}
	if options.MaxIdleConns < 0 {
		options.MaxIdleConns = 0
	} else if options.MaxIdleConns == 0 {
		options.MaxIdleConns = 4
	}

	vault, err := security.NewVault(options.DataDir)
	if err != nil {
		return nil, err
	}
	databasePath := filepath.Join(options.DataDir, options.DatabaseName)
	db, err := sql.Open("sqlite", buildDSN(databasePath, options.BusyTimeout))
	if err != nil {
		vault.Close()
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	db.SetMaxOpenConns(options.MaxOpenConns)
	db.SetMaxIdleConns(options.MaxIdleConns)
	db.SetConnMaxLifetime(0)

	store := &Store{db: db, vault: vault}
	if err := store.prepare(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("设置数据库权限: %w", err)
	}
	return store, nil
}

// Close 关闭数据库并清理进程内的密钥。
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.vault != nil {
		s.vault.Close()
	}
	return err
}

// UseProviderCredentials 在回调作用域内提供解密后的密钥，避免密钥进入公开返回对象。
func (s *Store) UseProviderCredentials(ctx context.Context, providerID int64, use func(provider domain.Provider, apiKey string) error) error {
	if use == nil {
		return fmt.Errorf("%w: 凭据回调不能为空", ErrInvalidInput)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, base_url, api_key_ciphertext, enabled, created_at, updated_at
		FROM providers
		WHERE id = ?`, providerID)
	provider, ciphertext, err := scanProviderWithCiphertext(row)
	if err != nil {
		return err
	}
	apiKey, err := s.vault.Decrypt(ciphertext)
	if err != nil {
		return fmt.Errorf("解密供应商密钥: %w", err)
	}
	return use(provider, apiKey)
}

func (s *Store) prepare(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("连接 SQLite: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("启用 SQLite WAL: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("启用 SQLite 外键: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
		return fmt.Errorf("设置 SQLite 同步策略: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		return err
	}
	return nil
}

func buildDSN(databasePath string, busyTimeout time.Duration) string {
	slashPath := filepath.ToSlash(databasePath)
	if filepath.IsAbs(databasePath) && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	uri := &url.URL{Scheme: "file", Path: slashPath}
	query := uri.Query()
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(NORMAL)")
	uri.RawQuery = query.Encode()
	return uri.String()
}
