package migrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
)

type LedgerInitializer struct{}

var _ database.TargetInitializer = LedgerInitializer{}

func (LedgerInitializer) EnsureReady(ctx context.Context, h database.TargetHandle) error {
	if h.DB == nil || !h.Driver.Valid() {
		return database.ErrTargetUnavailable
	}
	tx, err := beginTarget(ctx, h)
	if err != nil {
		return err
	}
	defer tx.close()
	if h.Driver == database.Postgres {
		err = ensurePostgresLedger(ctx, tx, h.EditorRole)
	} else {
		err = ensureSQLiteLedger(ctx, tx)
	}
	if err != nil {
		return err
	}
	return targetError(ctx, tx.commit(ctx))
}

func ensurePostgresLedger(ctx context.Context, q executor, role string) error {
	var safe bool
	if err := q.QueryRowContext(ctx, `SELECT NOT (rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls) AND rolname <> current_user FROM pg_roles WHERE rolname=$1`, role).Scan(&safe); err != nil || !safe {
		return fmt.Errorf("%w: editor role check (%v)", database.ErrLedgerIncompatible, err)
	}
	var exists bool
	// CREATE SCHEMA checks database privileges even when the schema already exists.
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='_platform')`).Scan(&exists); err != nil {
		return targetError(ctx, err)
	}
	if !exists {
		if _, err := q.ExecContext(ctx, `CREATE SCHEMA _platform`); err != nil {
			return fmt.Errorf("%w: ledger namespace unavailable", database.ErrLedgerIncompatible)
		}
	}
	for _, stmt := range []string{
		`REVOKE ALL ON SCHEMA _platform FROM PUBLIC, ` + postgres.QuoteIdent(role),
		`CREATE TABLE IF NOT EXISTS _platform.migration_ledger (
		 version bigint PRIMARY KEY CHECK (version BETWEEN 1 AND 9007199254740991),
		 run_id uuid NOT NULL UNIQUE, sql_hash text NOT NULL, sql_snapshot text NOT NULL,
		 actor_id uuid NOT NULL, actor_name_snapshot text NOT NULL, applied_at timestamptz NOT NULL)`,
		`REVOKE ALL ON TABLE _platform.migration_ledger FROM PUBLIC, ` + postgres.QuoteIdent(role),
	} {
		if _, err := q.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w: ledger setup: %w", database.ErrLedgerIncompatible, err)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull
	 FROM pg_attribute a WHERE a.attrelid='_platform.migration_ledger'::regclass AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`)
	if err != nil {
		return targetError(ctx, err)
	}
	expected := map[string]string{"version": "bigint", "run_id": "uuid", "sql_hash": "text", "sql_snapshot": "text", "actor_id": "uuid", "actor_name_snapshot": "text", "applied_at": "timestamp with time zone"}
	for rows.Next() {
		var name, typ string
		var notNull bool
		if err := rows.Scan(&name, &typ, &notNull); err != nil {
			rows.Close()
			return database.ErrLedgerIncompatible
		}
		if expected[name] != typ || !notNull {
			rows.Close()
			return database.ErrLedgerIncompatible
		}
		delete(expected, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return targetError(ctx, err)
	}
	if len(expected) != 0 {
		return database.ErrLedgerIncompatible
	}
	var keys int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint c
	 WHERE c.conrelid='_platform.migration_ledger'::regclass AND
	 ((c.contype='p' AND c.conkey=ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid=c.conrelid AND attname='version')])
	 OR (c.contype='u' AND c.conkey=ARRAY[(SELECT attnum FROM pg_attribute WHERE attrelid=c.conrelid AND attname='run_id')]))`).Scan(&keys)
	if err != nil {
		return targetError(ctx, err)
	}
	if keys != 2 {
		return fmt.Errorf("%w: expected primary and unique keys, found %d", database.ErrLedgerIncompatible, keys)
	}
	var exposed bool
	err = q.QueryRowContext(ctx, `SELECT has_schema_privilege($1, '_platform', 'USAGE') OR
	 has_table_privilege($1, '_platform.migration_ledger', 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')`, role).Scan(&exposed)
	if err != nil {
		return targetError(ctx, err)
	}
	if exposed {
		return fmt.Errorf("%w: editor can access ledger", database.ErrLedgerIncompatible)
	}
	if _, err := q.ExecContext(ctx, "SET LOCAL ROLE "+postgres.QuoteIdent(role)); err != nil {
		return fmt.Errorf("%w: editor role cannot be assumed", database.ErrLedgerIncompatible)
	}
	if _, err := q.ExecContext(ctx, "RESET ROLE"); err != nil {
		return targetError(ctx, err)
	}
	return nil
}

func ensureSQLiteLedger(ctx context.Context, q executor) error {
	_, err := q.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _platform_migration_ledger (
	 version INTEGER PRIMARY KEY CHECK (version BETWEEN 1 AND 9007199254740991),
	 run_id TEXT NOT NULL UNIQUE, sql_hash TEXT NOT NULL, sql_snapshot TEXT NOT NULL,
	 actor_id TEXT NOT NULL, actor_name_snapshot TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	if err != nil {
		return database.ErrLedgerIncompatible
	}
	rows, err := q.QueryContext(ctx, `SELECT name, type, "notnull", pk, hidden FROM pragma_table_xinfo('_platform_migration_ledger')`)
	if err != nil {
		return targetError(ctx, err)
	}
	expected := map[string]string{"version": "INTEGER", "run_id": "TEXT", "sql_hash": "TEXT", "sql_snapshot": "TEXT", "actor_id": "TEXT", "actor_name_snapshot": "TEXT", "applied_at": "TEXT"}
	for rows.Next() {
		var name, typ string
		var notNull, pk, hidden int
		if err := rows.Scan(&name, &typ, &notNull, &pk, &hidden); err != nil {
			rows.Close()
			return database.ErrLedgerIncompatible
		}
		if expected[name] != strings.ToUpper(typ) || hidden != 0 || (name == "version" && pk != 1) || (name != "version" && (notNull != 1 || pk != 0)) {
			rows.Close()
			return database.ErrLedgerIncompatible
		}
		delete(expected, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return targetError(ctx, err)
	}
	if len(expected) != 0 {
		return database.ErrLedgerIncompatible
	}
	var unique int
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM pragma_index_list('_platform_migration_ledger') il
	 WHERE il."unique"=1 AND il.partial=0 AND
	 (SELECT count(*) FROM pragma_index_info(il.name))=1 AND
	 (SELECT name FROM pragma_index_info(il.name))='run_id'`).Scan(&unique)
	if err != nil {
		return database.ErrLedgerIncompatible
	}
	if unique == 0 {
		return database.ErrLedgerIncompatible
	}
	return nil
}
