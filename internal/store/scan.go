package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// queryJSON runs q, which selects one column of JSON, and decodes each row
// into a T. what names a T in a decode error.
func queryJSON[T any](ctx context.Context, db *sql.DB, what, q string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			return nil, fmt.Errorf("decode %s: %w", what, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// queryStrings runs q, which selects one text column, and returns its
// values in order.
func queryStrings(ctx context.Context, db *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
