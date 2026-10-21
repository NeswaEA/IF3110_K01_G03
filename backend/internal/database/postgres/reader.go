package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

// Reader returns table rows for the viewer. It never accepts SQL: the table is
// looked up in the catalog first, identifiers are quoted, and only limit and
// offset are bound as parameters. Reads run in a READ ONLY transaction as the
// target's editor role, so a table that role may not read is ErrTargetPermission.
type Reader struct{}

// beginAsEditor opens the READ ONLY transaction viewer reads and plans run in:
// SET LOCAL ROLE to the handle's editor role, and statement_timeout bounded by
// the request deadline. Setup failures are the target's, never the user's.
func beginAsEditor(ctx context.Context, t database.TargetHandle) (*sql.Tx, error) {
	if t.EditorRole == "" {
		return nil, database.ErrTargetUnavailable // the registry always sets it; never fall back to the login role
	}
	tx, err := t.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, setupErr(ctx, err)
	}
	_, err = tx.ExecContext(ctx, `SET LOCAL ROLE `+QuoteIdent(t.EditorRole))
	if err == nil {
		if d, ok := ctx.Deadline(); ok {
			ms := max(time.Until(d).Milliseconds(), 1)
			_, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout', $1, true)`, strconv.FormatInt(ms, 10))
		}
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, setupErr(ctx, err)
	}
	return tx, nil
}

// setupErr: a role the login cannot switch to is a broken registration, not a 403.
func setupErr(ctx context.Context, err error) error {
	err = database.ClassifyTargetError(ctx, err)
	if errors.Is(err, database.ErrTargetPermission) {
		return database.ErrTargetUnavailable
	}
	return err
}

type column struct {
	name, dataType, typeName string
	enc                      database.Encoding
}

// encodingFor picks the JSON representation that keeps meaning and precision.
func encodingFor(typeName string) database.Encoding {
	switch typeName {
	case "int2", "int4", "oid":
		return database.EncNumber
	case "int8", "numeric", "float4", "float8", "money":
		return database.EncDecimalString // beyond 2^53, or NaN/Infinity, must not become a JS number
	case "bool":
		return database.EncBoolean
	case "timestamptz":
		return database.EncTimestamp
	case "json", "jsonb":
		return database.EncJSON
	case "bytea":
		return database.EncBase64
	}
	return database.EncText
}

func (Reader) ReadTable(ctx context.Context, t database.TargetHandle, schema, table string, limit, offset int) (database.TableData, bool, error) {
	if database.Reserved(schema, table) {
		return database.TableData{}, false, database.ErrNotFound // before any round trip
	}
	tx, err := beginAsEditor(ctx, t)
	if err != nil {
		return database.TableData{}, false, err
	}
	defer tx.Rollback()
	data, more, err := readTable(ctx, tx, schema, table, limit, offset)
	if err != nil {
		return database.TableData{}, false, database.ClassifyTargetError(ctx, err)
	}
	return data, more, nil
}

func readTable(ctx context.Context, tx *sql.Tx, schema, table string, limit, offset int) (database.TableData, bool, error) {
	var oid uint32
	// same visibility rule as the inspector: no system catalogs, no reserved schema
	err := tx.QueryRowContext(ctx, `
		SELECT t.oid FROM (`+userTables+`) t
		 WHERE t.nspname = $1 AND t.relname = $2`, schema, table).Scan(&oid)
	if err == sql.ErrNoRows {
		return database.TableData{}, false, fmt.Errorf("%w: table", database.ErrNotFound)
	}
	if err != nil {
		return database.TableData{}, false, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), ty.typname
		  FROM pg_attribute a JOIN pg_type ty ON ty.oid = a.atttypid
		 WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, oid)
	if err != nil {
		return database.TableData{}, false, err
	}
	var cols []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.dataType, &c.typeName); err != nil {
			rows.Close()
			return database.TableData{}, false, err
		}
		c.enc = encodingFor(c.typeName)
		cols = append(cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return database.TableData{}, false, err
	}

	var pkJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT json_agg(a.attname ORDER BY k.ord)
		                   FROM pg_index i, unnest(i.indkey::int2[]) WITH ORDINALITY k(attnum, ord)
		                   JOIN pg_attribute a ON a.attrelid = $1 AND a.attnum = k.attnum
		                  WHERE i.indrelid = $1 AND i.indisprimary), '[]')::text`, oid).Scan(&pkJSON); err != nil {
		return database.TableData{}, false, err
	}
	var pk []string
	if err := json.Unmarshal([]byte(pkJSON), &pk); err != nil {
		return database.TableData{}, false, err
	}

	data := database.TableData{Columns: []database.DataColumn{}, Rows: [][]any{}, OrderStable: len(pk) > 0}
	selects := make([]string, len(cols))
	for i, c := range cols {
		data.Columns = append(data.Columns, database.DataColumn{Name: c.name, DataType: c.dataType, Encoding: c.enc})
		q := QuoteIdent(c.name)
		switch c.enc {
		case database.EncBase64:
			selects[i] = q
		case database.EncTimestamp:
			selects[i] = `to_char(` + q + ` AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
		default:
			selects[i] = q + `::text`
		}
	}
	if len(cols) == 0 {
		return data, false, nil
	}
	query := `SELECT ` + strings.Join(selects, ", ") + ` FROM ` + qualified(schema, table)
	if len(pk) > 0 {
		quoted := make([]string, len(pk))
		for i, p := range pk {
			quoted[i] = QuoteIdent(p)
		}
		query += ` ORDER BY ` + strings.Join(quoted, ", ")
	}
	query += ` LIMIT $1 OFFSET $2`

	rows, err = tx.QueryContext(ctx, query, limit+1, offset)
	if err != nil {
		return database.TableData{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		texts := make([]sql.NullString, len(cols))
		bins := make([][]byte, len(cols))
		dest := make([]any, len(cols))
		for i, c := range cols {
			if c.enc == database.EncBase64 {
				dest[i] = &bins[i]
			} else {
				dest[i] = &texts[i]
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return database.TableData{}, false, err
		}
		row := make([]any, len(cols))
		for i, c := range cols {
			if c.enc == database.EncBase64 {
				if bins[i] != nil {
					row[i] = base64.StdEncoding.EncodeToString(bins[i])
				}
				continue
			}
			if !texts[i].Valid {
				continue // NULL stays null whatever the encoding
			}
			row[i] = convert(c.enc, texts[i].String)
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

func convert(enc database.Encoding, s string) any {
	switch enc {
	case database.EncNumber:
		return json.Number(s)
	case database.EncBoolean:
		return s == "true" || s == "t"
	case database.EncJSON:
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
	}
	return s
}
