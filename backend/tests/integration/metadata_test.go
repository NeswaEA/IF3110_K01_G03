package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMetadataSchema(t *testing.T) {
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open metadata database failed")
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("begin metadata transaction failed")
	}
	defer tx.Rollback()
	schema := "test_metadata_" + strings.ToLower(rand.Text())
	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path TO "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"0001_identity.sql", "0002_connections.sql", "0003_sessions_access.sql", "0004_migrations.sql"} {
		ddl, err := os.ReadFile(filepath.Join("..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(ddl)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const userID = "10000000-0000-4000-8000-000000000003"
	const groupID = "20000000-0000-4000-8000-000000000002"
	const databaseID = "30000000-0000-4000-8000-000000000001"
	const sessionID = "60000000-0000-4000-8000-000000000001"
	const unknownID = "90000000-0000-4000-8000-000000000001"
	hash := "$argon2id$v=19$m=19456,t=2,p=1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)
	exec("INSERT INTO users (id, username, password_hash) VALUES ($1, 'editor', $2)", userID, hash)
	exec("INSERT INTO groups (id, name) VALUES ($1, 'Editors')", groupID)
	exec("INSERT INTO groups (id, name) VALUES ($1, 'editors')", unknownID)
	exec("INSERT INTO database_connections (id, name, driver, config_encrypted) VALUES ($1, 'Demo', 'postgres', $2)", databaseID, []byte{1})
	exec("INSERT INTO group_memberships (group_id, user_id) VALUES ($1, $2), ($3, $2)", groupID, userID, unknownID)
	exec("INSERT INTO group_database_access (group_id, database_id, level) VALUES ($1, $2, 'edit')", groupID, databaseID)
	exec("INSERT INTO sessions (id, user_id, absolute_expires_at, csrf_secret) VALUES ($1, $2, now() + interval '8 hours', $3)", sessionID, userID, bytes.Repeat([]byte{1}, 32))
	exec("INSERT INTO sessions (id, absolute_expires_at, csrf_secret) VALUES ($1, now() + interval '10 minutes', $2)", unknownID, bytes.Repeat([]byte{2}, 32))
	exec("INSERT INTO session_tokens (token_hash, session_id, generation, is_current, valid_until) VALUES ($1, $2, 1, true, now() + interval '8 hours')", bytes.Repeat([]byte{1}, 32), sessionID)
	exec("INSERT INTO session_tokens (token_hash, session_id, generation, is_current, valid_until) VALUES ($1, $2, 1, false, now() + interval '30 seconds')", bytes.Repeat([]byte{2}, 32), sessionID)
	t.Run("defaults", func(t *testing.T) {
		var active, admin, rotation bool
		var generation int
		err := tx.QueryRowContext(ctx, "SELECT u.active, u.is_platform_admin, s.rotation_required, s.generation FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.id=$1", sessionID).Scan(&active, &admin, &rotation, &generation)
		if err != nil || !active || admin || rotation || generation != 1 {
			t.Fatal("incorrect identity or session defaults")
		}
	})
	for _, tt := range []struct {
		name, query, code string
		args              []any
	}{
		{"uppercase username", "UPDATE users SET username='Editor' WHERE id=$1", "23514", []any{userID}},
		{"short username", "UPDATE users SET username='ab' WHERE id=$1", "23514", []any{userID}},
		{"long username", "UPDATE users SET username=$2 WHERE id=$1", "23514", []any{userID, strings.Repeat("a", 65)}},
		{"username whitespace", "UPDATE users SET username=' editor ' WHERE id=$1", "23514", []any{userID}},
		{"duplicate username", "INSERT INTO users (id, username, password_hash) VALUES ($1, 'editor', $2)", "23505", []any{unknownID, hash}},
		{"plaintext password", "UPDATE users SET password_hash='plaintext' WHERE id=$1", "23514", []any{userID}},
		{"empty group name", "UPDATE groups SET name='' WHERE id=$1", "23514", []any{groupID}},
		{"untrimmed group name", "UPDATE groups SET name=' Editors ' WHERE id=$1", "23514", []any{groupID}},
		{"duplicate group name", "UPDATE groups SET name='Editors' WHERE id=$1", "23505", []any{unknownID}},
		{"duplicate membership", "INSERT INTO group_memberships (group_id, user_id) VALUES ($1,$2)", "23505", []any{groupID, userID}},
		{"missing member", "INSERT INTO group_memberships (group_id, user_id) VALUES ($1,$2)", "23503", []any{groupID, unknownID}},
		{"missing group", "INSERT INTO group_memberships (group_id, user_id) VALUES ($1,$2)", "23503", []any{sessionID, userID}},
		{"none grant", "UPDATE group_database_access SET level='none' WHERE group_id=$1", "23514", []any{groupID}},
		{"duplicate grant", "INSERT INTO group_database_access (group_id,database_id,level) VALUES ($1,$2,'view')", "23505", []any{groupID, databaseID}},
		{"missing database", "INSERT INTO group_database_access (group_id,database_id,level) VALUES ($1,$2,'view')", "23503", []any{groupID, unknownID}},
		{"short csrf", "UPDATE sessions SET csrf_secret=$2 WHERE id=$1", "23514", []any{sessionID, []byte{1}}},
		{"invalid generation", "UPDATE sessions SET generation=0 WHERE id=$1", "23514", []any{sessionID}},
		{"absolute deadline mutation", "UPDATE sessions SET absolute_expires_at=absolute_expires_at+interval '1 hour' WHERE id=$1", "23514", []any{sessionID}},
		{"activity after expiry", "UPDATE sessions SET last_seen_at=absolute_expires_at+interval '1 second' WHERE id=$1", "23514", []any{sessionID}},
		{"short token hash", "UPDATE session_tokens SET token_hash=$1 WHERE is_current", "23514", []any{[]byte{1}}},
		{"duplicate current token", "UPDATE session_tokens SET is_current=true WHERE NOT is_current", "23505", nil},
		{"invalid token generation", "UPDATE session_tokens SET generation=0", "23514", nil},
		{"token expiry before creation", "UPDATE session_tokens SET valid_until=created_at-interval '1 second'", "23514", nil},
		{"referenced user deletion", "DELETE FROM users WHERE id=$1", "23503", []any{userID}},
		{"referenced database deletion", "DELETE FROM database_connections WHERE id=$1", "23503", []any{databaseID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exec("SAVEPOINT invalid_input")
			_, err := tx.ExecContext(ctx, tt.query, tt.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tt.code {
				t.Errorf("expected SQLSTATE %s, got %v", tt.code, err)
			}
			exec("ROLLBACK TO SAVEPOINT invalid_input")
			exec("RELEASE SAVEPOINT invalid_input")
		})
	}
	t.Run("group deletion cleans memberships and grants", func(t *testing.T) {
		exec("DELETE FROM groups WHERE id=$1", groupID)
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM group_memberships WHERE group_id=$1)+(SELECT count(*) FROM group_database_access WHERE group_id=$1)", groupID).Scan(&count); err != nil || count != 0 {
			t.Fatal("group children remained")
		}
	})
	t.Run("session deletion cleans tokens", func(t *testing.T) {
		exec("DELETE FROM sessions WHERE id=$1", sessionID)
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM session_tokens WHERE session_id=$1", sessionID).Scan(&count); err != nil || count != 0 {
			t.Fatal("session tokens remained")
		}
	})
}
