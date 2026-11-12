package migrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
)

// HistoryInitializer creates and verifies the reserved migration history (and,
// on PostgreSQL, the batch checkpoints) on a target. It refuses a target that
// still carries the earlier ledger, which must be reset instead of upgraded.
type HistoryInitializer struct{}

var _ database.TargetInitializer = HistoryInitializer{}

func (HistoryInitializer) EnsureReady(ctx context.Context, h database.TargetHandle) error {
	if h.DB == nil || !h.Driver.Valid() {
		return database.ErrTargetUnavailable
	}
	tx, err := beginTarget(ctx, h)
	if err != nil {
		return err
	}
	defer tx.close()
	if h.Driver == database.Postgres {
		err = ensurePostgresHistory(ctx, tx, h.EditorRole)
	} else {
		err = ensureSQLiteHistory(ctx, tx)
	}
	if err != nil {
		return err
	}
	return targetError(ctx, tx.commit(ctx))
}

type column struct {
	typ      string
	nullable bool
}

var postgresHistoryColumns = map[string]column{
	"seq": {typ: "bigint"}, "execution_id": {typ: "uuid"}, "migration_id": {typ: "uuid"},
	"migration_revision": {typ: "bigint"}, "action": {typ: "text"}, "version": {typ: "bigint"},
	"head_after": {typ: "bigint"}, "reverts_execution_id": {typ: "uuid", nullable: true},
	"sql_executed": {typ: "text"}, "sql_hash": {typ: "text"}, "mode": {typ: "text"},
	"batches": {typ: "integer"}, "rows": {typ: "bigint"}, "actor_id": {typ: "uuid"},
	"actor_name": {typ: "text"}, "committed_at": {typ: "timestamp with time zone"},
}

var postgresCheckpointColumns = map[string]column{
	"execution_id": {typ: "uuid"}, "batch_no": {typ: "integer"}, "chain_root_id": {typ: "uuid"},
	"direction": {typ: "text"}, "cursor_from": {typ: "bigint"}, "cursor_to": {typ: "bigint"},
	"rows": {typ: "integer"}, "committed_at": {typ: "timestamp with time zone"},
}

func ensurePostgresHistory(ctx context.Context, q executor, role string) error {
	var safe bool
	if err := q.QueryRowContext(ctx, `SELECT NOT (rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls) AND rolname <> current_user FROM pg_roles WHERE rolname=$1`, role).Scan(&safe); err != nil || !safe {
		return fmt.Errorf("%w: editor role check (%v)", database.ErrLedgerIncompatible, err)
	}
	var schemaExists, oldLedger bool
	// CREATE SCHEMA checks database privileges even when the schema already exists.
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='_platform'), to_regclass('_platform.migration_ledger') IS NOT NULL`).Scan(&schemaExists, &oldLedger); err != nil {
		return targetError(ctx, err)
	}
	if oldLedger {
		return fmt.Errorf("%w: earlier ledger found; reset the target", database.ErrLedgerIncompatible)
	}
	if !schemaExists {
		if _, err := q.ExecContext(ctx, `CREATE SCHEMA _platform`); err != nil {
			return fmt.Errorf("%w: history namespace unavailable", database.ErrLedgerIncompatible)
		}
	}
	editor := postgres.QuoteIdent(role)
	for _, stmt := range []string{
		`REVOKE ALL ON SCHEMA _platform FROM PUBLIC, ` + editor,
		`CREATE TABLE IF NOT EXISTS _platform.migration_history (
		 seq bigint PRIMARY KEY CHECK (seq >= 1), execution_id uuid NOT NULL UNIQUE,
		 migration_id uuid NOT NULL, migration_revision bigint NOT NULL,
		 action text NOT NULL CHECK (action IN ('apply','rollback','forward')),
		 version bigint NOT NULL, head_after bigint NOT NULL, reverts_execution_id uuid,
		 sql_executed text NOT NULL, sql_hash text NOT NULL, mode text NOT NULL,
		 batches integer NOT NULL, rows bigint NOT NULL, actor_id uuid NOT NULL,
		 actor_name text NOT NULL, committed_at timestamptz NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS _platform.migration_checkpoints (
		 execution_id uuid NOT NULL, batch_no integer NOT NULL, chain_root_id uuid NOT NULL,
		 direction text NOT NULL CHECK (direction IN ('up','down')),
		 cursor_from bigint NOT NULL, cursor_to bigint NOT NULL, rows integer NOT NULL,
		 committed_at timestamptz NOT NULL, PRIMARY KEY (execution_id, batch_no))`,
		`REVOKE ALL ON TABLE _platform.migration_history, _platform.migration_checkpoints FROM PUBLIC, ` + editor,
		`CREATE OR REPLACE FUNCTION _platform.protect_migration_history() RETURNS trigger LANGUAGE plpgsql AS $$
		  BEGIN RAISE EXCEPTION 'migration history is append-only' USING ERRCODE = '23514'; END $$`,
		`CREATE OR REPLACE TRIGGER migration_history_append_only
		  BEFORE UPDATE OR DELETE OR TRUNCATE ON _platform.migration_history
		  FOR EACH STATEMENT EXECUTE FUNCTION _platform.protect_migration_history()`,
	} {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w: history setup: %w", database.ErrLedgerIncompatible, err)
		}
	}
	for _, table := range []struct {
		name    string
		columns map[string]column
		keys    [][2]string // constraint type, comma-separated columns
	}{
		{"_platform.migration_history", postgresHistoryColumns, [][2]string{{"p", "seq"}, {"u", "execution_id"}}},
		{"_platform.migration_checkpoints", postgresCheckpointColumns, [][2]string{{"p", "execution_id,batch_no"}}},
	} {
		if err := verifyPostgresColumns(ctx, q, table.name, table.columns); err != nil {
			return err
		}
		for _, key := range table.keys {
			var found bool
			err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid=$1::regclass AND c.contype=$2 AND
			 (SELECT string_agg(a.attname, ',' ORDER BY k.ord) FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
			  JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum) = $3)`, table.name, key[0], key[1]).Scan(&found)
			if err != nil {
				return targetError(ctx, err)
			}
			if !found {
				return fmt.Errorf("%w: %s lacks key (%s)", database.ErrLedgerIncompatible, table.name, key[1])
			}
		}
	}
	var exposed bool
	err := q.QueryRowContext(ctx, `SELECT has_schema_privilege($1, '_platform', 'USAGE') OR
	 has_table_privilege($1, '_platform.migration_history', 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') OR
	 has_table_privilege($1, '_platform.migration_checkpoints', 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')`, role).Scan(&exposed)
	if err != nil {
		return targetError(ctx, err)
	}
	if exposed {
		return fmt.Errorf("%w: editor can access the history", database.ErrLedgerIncompatible)
	}
	if _, err := q.ExecContext(ctx, "SET LOCAL ROLE "+editor); err != nil {
		return fmt.Errorf("%w: editor role cannot be assumed", database.ErrLedgerIncompatible)
	}
	if _, err := q.ExecContext(ctx, "RESET ROLE"); err != nil {
		return targetError(ctx, err)
	}
	return nil
}

func verifyPostgresColumns(ctx context.Context, q executor, table string, expected map[string]column) error {
	rows, err := q.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull
	 FROM pg_attribute a WHERE a.attrelid=$1::regclass AND a.attnum>0 AND NOT a.attisdropped`, table)
	if err != nil {
		return targetError(ctx, err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, typ string
		var notNull bool
		if err := rows.Scan(&name, &typ, &notNull); err != nil {
			return database.ErrLedgerIncompatible
		}
		want, ok := expected[name]
		if !ok || want.typ != typ || want.nullable == notNull {
			return fmt.Errorf("%w: %s.%s has an unexpected shape", database.ErrLedgerIncompatible, table, name)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return targetError(ctx, err)
	}
	if seen != len(expected) {
		return fmt.Errorf("%w: %s is missing columns", database.ErrLedgerIncompatible, table)
	}
	return nil
}

var sqliteHistoryColumns = map[string]column{
	"seq": {typ: "INTEGER"}, "execution_id": {typ: "TEXT"}, "migration_id": {typ: "TEXT"},
	"migration_revision": {typ: "INTEGER"}, "action": {typ: "TEXT"}, "version": {typ: "INTEGER"},
	"head_after": {typ: "INTEGER"}, "reverts_execution_id": {typ: "TEXT", nullable: true},
	"sql_executed": {typ: "TEXT"}, "sql_hash": {typ: "TEXT"}, "mode": {typ: "TEXT"},
	"batches": {typ: "INTEGER"}, "rows": {typ: "INTEGER"}, "actor_id": {typ: "TEXT"},
	"actor_name": {typ: "TEXT"}, "committed_at": {typ: "TEXT"},
}

func ensureSQLiteHistory(ctx context.Context, q executor) error {
	var oldLedger int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='_platform_migration_ledger'`).Scan(&oldLedger); err != nil {
		return targetError(ctx, err)
	}
	if oldLedger != 0 {
		return fmt.Errorf("%w: earlier ledger found; reset the target", database.ErrLedgerIncompatible)
	}
	_, err := q.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _platform_migration_history (
	 seq INTEGER PRIMARY KEY CHECK (seq >= 1), execution_id TEXT NOT NULL UNIQUE,
	 migration_id TEXT NOT NULL, migration_revision INTEGER NOT NULL,
	 action TEXT NOT NULL CHECK (action IN ('apply','rollback','forward')),
	 version INTEGER NOT NULL, head_after INTEGER NOT NULL, reverts_execution_id TEXT,
	 sql_executed TEXT NOT NULL, sql_hash TEXT NOT NULL, mode TEXT NOT NULL,
	 batches INTEGER NOT NULL, rows INTEGER NOT NULL, actor_id TEXT NOT NULL,
	 actor_name TEXT NOT NULL, committed_at TEXT NOT NULL)`)
	if err != nil {
		return database.ErrLedgerIncompatible
	}
	for _, trigger := range []string{
		`CREATE TRIGGER IF NOT EXISTS _platform_migration_history_no_update BEFORE UPDATE ON _platform_migration_history
		  BEGIN SELECT RAISE(ABORT, 'migration history is append-only'); END`,
		`CREATE TRIGGER IF NOT EXISTS _platform_migration_history_no_delete BEFORE DELETE ON _platform_migration_history
		  BEGIN SELECT RAISE(ABORT, 'migration history is append-only'); END`,
	} {
		if _, err := q.ExecContext(ctx, trigger); err != nil {
			return database.ErrLedgerIncompatible
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT name, type, "notnull", pk, hidden FROM pragma_table_xinfo('_platform_migration_history')`)
	if err != nil {
		return targetError(ctx, err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name, typ string
		var notNull, pk, hidden int
		if err := rows.Scan(&name, &typ, &notNull, &pk, &hidden); err != nil {
			return database.ErrLedgerIncompatible
		}
		want, ok := sqliteHistoryColumns[name]
		// INTEGER PRIMARY KEY is the rowid alias and reports notnull=0.
		isKey := name == "seq"
		if !ok || want.typ != strings.ToUpper(typ) || hidden != 0 || isKey != (pk == 1) || (!isKey && want.nullable == (notNull == 1)) {
			return fmt.Errorf("%w: _platform_migration_history.%s has an unexpected shape", database.ErrLedgerIncompatible, name)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return targetError(ctx, err)
	}
	if seen != len(sqliteHistoryColumns) {
		return fmt.Errorf("%w: _platform_migration_history is missing columns", database.ErrLedgerIncompatible)
	}
	var unique int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM pragma_index_list('_platform_migration_history') il
	 WHERE il."unique"=1 AND il.partial=0 AND
	 (SELECT count(*) FROM pragma_index_info(il.name))=1 AND
	 (SELECT name FROM pragma_index_info(il.name))='execution_id'`).Scan(&unique)
	if err != nil || unique == 0 {
		return fmt.Errorf("%w: execution_id is not unique", database.ErrLedgerIncompatible)
	}
	return nil
}
