package main

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPrepareLoadTableIsIdempotentAndOwnedByEditor(t *testing.T) {
	dsn := os.Getenv("TEST_TARGET_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_TARGET_DATABASE_URL is required in CI")
		}
		t.Skip("set TEST_TARGET_DATABASE_URL to run the load seed test")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A temporary table on one session keeps the shared demo database untouched.
	db.SetMaxOpenConns(1)
	for range 2 {
		if err := prepareLoadTable(t.Context(), db, "platform_editor", "pg_temp"); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	var owner string
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM pg_temp.load_items), tableowner FROM pg_tables
		WHERE schemaname = pg_my_temp_schema()::regnamespace::text AND tablename = 'load_items'`).Scan(&rows, &owner); err != nil {
		t.Fatal(err)
	}
	if rows != loadRows || owner != "platform_editor" {
		t.Fatalf("rows %d owner %s, want %d platform_editor", rows, owner, loadRows)
	}
}
