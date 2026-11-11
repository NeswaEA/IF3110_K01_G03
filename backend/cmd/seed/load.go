package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
)

// The registered stress-test targets (databases load_1..load_4).
var loadDatabases = []string{
	"30000000-0000-4000-8000-000000000011", "30000000-0000-4000-8000-000000000012",
	"30000000-0000-4000-8000-000000000013", "30000000-0000-4000-8000-000000000014",
}

const loadRows = 1000

// seedLoad gives every stress-test target a small load_items table that the
// k6 migrations update. Re-running keeps existing tables and rows.
func seedLoad(ctx context.Context, meta *sql.DB) error {
	cipher, err := connections.NewCipher(os.Getenv("CONNECTION_ENCRYPTION_KEY"))
	if err != nil {
		return errors.New("CONNECTION_ENCRYPTION_KEY must encode 32 bytes in base64")
	}
	registry := connections.NewRegistry(meta, cipher, connections.RegistryOptions{SQLiteDir: os.TempDir()})
	defer registry.Close()
	for _, id := range loadDatabases {
		target, release, err := registry.Acquire(ctx, id)
		if err != nil {
			return fmt.Errorf("load target %s is not registered or unreachable; run `seed fixtures` first", id)
		}
		err = prepareLoadTable(ctx, target.DB, target.EditorRole, "public")
		release()
		if err != nil {
			return fmt.Errorf("load target %s: %w", id, err)
		}
	}
	return nil
}

// prepareLoadTable creates schema.load_items as the editor role, so the
// migrations k6 submits (which run as that role) own and may change it.
func prepareLoadTable(ctx context.Context, db *sql.DB, editorRole, schema string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	quote := func(s string) string { return `"` + s + `"` }
	for _, stmt := range []string{
		`SET LOCAL ROLE ` + quote(editorRole),
		`CREATE TABLE IF NOT EXISTS ` + quote(schema) + `.load_items (
			id bigint PRIMARY KEY,
			name text NOT NULL,
			qty integer NOT NULL DEFAULT 0,
			updated_at timestamptz NOT NULL DEFAULT now())`,
		fmt.Sprintf(`INSERT INTO %s.load_items (id, name) SELECT g, 'item ' || g FROM generate_series(1, %d) g
			ON CONFLICT (id) DO NOTHING`, quote(schema), loadRows),
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
