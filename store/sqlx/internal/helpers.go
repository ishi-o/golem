package sqlx

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/store"
)

func (s *Store) rebind(query string) string { return s.db.Rebind(query) }

func (s *Store) upsert(table string, columns, updates []string) string {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), placeholders)
	switch s.dialect {
	case DialectMySQL:
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			assignments = append(assignments, column+" = VALUES("+column+")")
		}
		return query + " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", ")
	case DialectGeneric:
		// Generic SQL has no portable upsert. The caller should use a
		// dialect for stores that need concurrent updates; this clause is
		// kept useful for engines that accept SQL-standard MERGE-like
		// aliases through ON CONFLICT.
		fallthrough
	default:
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			if column == columns[0] {
				continue
			}
			assignments = append(assignments, column+" = excluded."+column)
		}
		return query + " ON CONFLICT (" + columns[0] + ") DO UPDATE SET " + strings.Join(assignments, ", ")
	}
}

func marshal(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	return string(data), err
}

func unmarshal(value sql.NullString, target any) error {
	if !value.Valid || value.String == "" || value.String == "null" {
		return nil
	}
	return json.Unmarshal([]byte(value.String), target)
}

func timeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value.String)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlx store: %s: %w", operation, err)
}

var (
	_ store.Backend         = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)
