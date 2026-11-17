package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
)

func bigDataRows(args []string) (int64, error) {
	if len(args) == 0 {
		return 5_000_000, nil
	}
	if len(args) == 2 && args[0] == "--rows" {
		n, err := strconv.ParseInt(args[1], 10, 64)
		if err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, errors.New("usage: seed bigdata [--rows N], where N is a positive integer")
}

func seedBigData(ctx context.Context, meta *sql.DB, count int64) error {
	cipher, err := connections.NewCipher(os.Getenv("CONNECTION_ENCRYPTION_KEY"))
	if err != nil {
		return errors.New("CONNECTION_ENCRYPTION_KEY must encode 32 bytes in base64")
	}
	registry := connections.NewRegistry(meta, cipher, connections.RegistryOptions{SQLiteDir: os.TempDir()})
	defer registry.Close()
	target, release, err := registry.Acquire(ctx, bigDatabase)
	if err != nil {
		return errors.New("demo_big is unreachable; run seed fixtures first")
	}
	defer release()
	if target.Driver != database.Postgres || target.EditorRole == "" {
		return errors.New("demo_big requires PostgreSQL and an editor role")
	}
	tx, err := target.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"SELECT pg_advisory_xact_lock(" + postgres.MigrationLockKey + ")",
		"SET LOCAL ROLE " + postgres.QuoteIdent(target.EditorRole),
		`CREATE TABLE IF NOT EXISTS public.big_items (
			id bigint PRIMARY KEY, price numeric(12,2) NOT NULL,
			status text NOT NULL, updated_at timestamptz NOT NULL)`,
		"LOCK TABLE public.big_items IN SHARE ROW EXCLUSIVE MODE",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("prepare big_items: %w", err)
		}
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM public.big_items)").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `INSERT INTO public.big_items(id,price,status,updated_at)
			SELECT id, (100 + id % 1000)::numeric(12,2),'active',now() FROM generate_series(1::bigint,$1) AS id`, count); err != nil {
			return fmt.Errorf("populate big_items: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "ANALYZE public.big_items"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
