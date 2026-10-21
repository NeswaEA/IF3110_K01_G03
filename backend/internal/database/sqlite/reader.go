package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Reader returns table rows for the viewer on a pinned, read-only connection.
// The table must exist in sqlite_schema; only LIMIT/OFFSET are bound.
type Reader struct{}

// affinity follows SQLite's rules for a declared type
// (https://www.sqlite.org/datatype3.html#determination_of_column_affinity).
func affinity(declared string) database.Encoding {
	t := strings.ToUpper(declared)
	switch {
	case strings.Contains(t, "INT"):
		return database.EncDecimalString // 64-bit integers exceed 2^53
	case t == "", strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"):
		return database.EncText // untyped columns usually hold text; non-UTF-8 bytes fall back to base64
	case strings.Contains(t, "BLOB"):
		return database.EncBase64
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUB"):
		return database.EncNumber
	}
	return database.EncDecimalString // NUMERIC affinity
}

func (Reader) ReadTable(ctx context.Context, t database.TargetHandle, schema, table string, limit, offset int) (database.TableData, bool, error) {
	if schema != SchemaName || database.Reserved(schema, table) {
		return database.TableData{}, false, fmt.Errorf("%w: table", database.ErrNotFound)
	}
	var data database.TableData
	var more bool
	err := withReadOnlyConn(ctx, t.DB, func(tx *sql.Tx) error {
		var err error
		data, more, err = readTable(ctx, tx, table, limit, offset)
		return err
	})
	if err != nil {
		return database.TableData{}, false, database.ClassifyTargetError(ctx, err)
	}
	return data, more, nil
}

func readTable(ctx context.Context, tx *sql.Tx, table string, limit, offset int) (database.TableData, bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
		return database.TableData{}, false, err
	}
	if exists == 0 || strings.HasPrefix(table, "sqlite_") {
		return database.TableData{}, false, fmt.Errorf("%w: table", database.ErrNotFound)
	}
	info := tableInfo{name: table}
	if err := loadColumns(ctx, tx, &info); err != nil {
		return database.TableData{}, false, err
	}

	data := database.TableData{Columns: []database.DataColumn{}, Rows: [][]any{}, OrderStable: len(info.pk) > 0}
	encs := make([]database.Encoding, len(info.cols))
	names := make([]string, len(info.cols))
	for i, c := range info.cols {
		encs[i] = affinity(c.DataType)
		names[i] = c.Name
		data.Columns = append(data.Columns, database.DataColumn{Name: c.Name, DataType: c.DataType, Encoding: encs[i]})
	}
	if len(names) == 0 {
		return data, false, nil
	}
	query := `SELECT ` + quoteList(names) + ` FROM "main".` + QuoteIdent(table)
	if len(info.pk) > 0 {
		query += ` ORDER BY ` + quoteList(info.pk)
	}
	query += ` LIMIT ? OFFSET ?`

	rows, err := tx.QueryContext(ctx, query, limit+1, offset)
	if err != nil {
		return database.TableData{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range vals {
			dest[i] = &vals[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return database.TableData{}, false, err
		}
		row := make([]any, len(names))
		for i, v := range vals {
			row[i] = convert(encs[i], v)
		}
		data.Rows = append(data.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return database.TableData{}, false, err
	}
	more := len(data.Rows) > limit
	if more {
		data.Rows = data.Rows[:limit]
	}
	return data, more, nil
}

// convert renders one dynamically typed SQLite value in the column's encoding.
// SQLite does not enforce declared types, so a stored value may differ from
// the affinity; it is then kept as faithfully as possible instead of dropped.
func convert(enc database.Encoding, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		if enc == database.EncNumber && x > -1<<53 && x < 1<<53 {
			return json.Number(strconv.FormatInt(x, 10))
		}
		return strconv.FormatInt(x, 10)
	case float64:
		if enc == database.EncNumber {
			return x
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		if enc == database.EncBase64 {
			return base64.StdEncoding.EncodeToString([]byte(x))
		}
		return x
	case []byte:
		if enc != database.EncBase64 && utf8.Valid(x) {
			return string(x)
		}
		return base64.StdEncoding.EncodeToString(x)
	}
	return fmt.Sprint(v)
}
