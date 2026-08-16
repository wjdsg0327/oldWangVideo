package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const storageTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

type rowScanner interface {
	Scan(dest ...any) error
}

func nowText() string {
	return time.Now().UTC().Format(storageTimeFormat)
}

func timeText(value time.Time) string {
	return value.UTC().Format(storageTimeFormat)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析数据库时间 %q: %w", value, err)
	}
	return parsed, nil
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func mapDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint failed") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if strings.Contains(message, "foreign key constraint failed") {
		return fmt.Errorf("%w: 关联记录不存在或仍被引用", ErrConflict)
	}
	return err
}
