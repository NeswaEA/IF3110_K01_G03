package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
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
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil || len(files) != 8 {
		t.Fatalf("expected 8 metadata migrations, got %d (%v)", len(files), err)
	}
	sort.Strings(files)
	for _, file := range files {
		ddl, err := os.ReadFile(file)
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
	expectSQLState := func(t *testing.T, code, query string, args ...any) {
		t.Helper()
		exec("SAVEPOINT invalid_input")
		_, err := tx.ExecContext(ctx, query, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Errorf("expected SQLSTATE %s, got %v", code, err)
		}
		exec("ROLLBACK TO SAVEPOINT invalid_input")
		exec("RELEASE SAVEPOINT invalid_input")
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

	const viewerID = "10000000-0000-4000-8000-000000000002"
	const migrationID = "40000000-0000-4000-8000-000000000001"
	const pendingID = "40000000-0000-4000-8000-000000000002"
	const executionID = "50000000-0000-4000-8000-000000000001"
	const dryRunID = "50000000-0000-4000-8000-000000000002"
	const otherDryRunID = "50000000-0000-4000-8000-000000000003"
	const unknownOutcomeID = "50000000-0000-4000-8000-000000000004"
	const requestID = "70000000-0000-4000-8000-000000000001"
	const openRequestID = "70000000-0000-4000-8000-000000000002"
	const approvalID = "80000000-0000-4000-8000-000000000001"
	contentHash := bytes.Repeat([]byte{3}, 32)
	const validBatch = `{"schema":"public","table":"items","key_column":"id","batch_size":10,"pause_ms":0}`
	insertRevision := "INSERT INTO migration_revisions (migration_id, revision, up_sql, down_sql, irreversible_reason, mode, batch, content_hash, created_by, created_at) VALUES ($1, $2, 'UPDATE items SET note = 1', $3, $4, $5, $6, $7, $8, now())"
	insertRequest := "INSERT INTO change_requests (id, database_id, migration_id, action, migration_revision, content_hash, head_at_request, target_execution_id, requested_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8, now(), now())"
	insertExecution := "INSERT INTO migration_executions (id, database_id, number, migration_id, change_request_id, action, direction, mode, migration_revision, content_hash, status, max_attempts, chain_root_id, enqueued_by, enqueued_by_name, enqueued_at) VALUES ($1, $2, $3, $4, $5, $6, 'up', 'normal', 1, $7, $8, 3, $1, $9, 'editor', now())"
	insertHistory := "INSERT INTO migration_history (database_id, seq, execution_id, migration_id, migration_revision, action, version, head_after, reverts_execution_id, sql_executed, sql_hash, mode, actor_id, actor_name, committed_at) VALUES ($1, $2, $3, $4, 1, $5, 1, 1, NULL, 'ALTER TABLE items ADD COLUMN note text', $6, 'normal', $7, 'editor', now())"
	insertApproval := "INSERT INTO change_request_approvals (id, request_id, group_id, approver_id, decision, comment, migration_revision, created_at) VALUES ($1, $2, $3, $4, $5, $6, 1, now())"
	exec("INSERT INTO users (id, username, password_hash) VALUES ($1, 'viewer', $2)", viewerID, hash)
	exec("UPDATE groups SET leader_user_id=$2 WHERE id=$1", groupID, userID)
	exec("INSERT INTO migrations (id, database_id, title, status, version, locked_revision, locked_at, created_by, updated_by, created_at, updated_at) VALUES ($1, $2, 'Add note', 'applied', 1, 1, now(), $3, $3, now(), now())", migrationID, databaseID, userID)
	exec("INSERT INTO migrations (id, database_id, title, created_by, updated_by, created_at, updated_at) VALUES ($1, $2, 'Fill note', $3, $3, now(), now())", pendingID, databaseID, userID)
	exec(insertRevision, migrationID, 1, "ALTER TABLE items DROP COLUMN note", nil, "normal", nil, contentHash, userID)
	exec(insertRevision, pendingID, 1, "UPDATE items SET note = NULL", nil, "normal", nil, contentHash, userID)
	exec("INSERT INTO change_requests (id, database_id, migration_id, action, migration_revision, content_hash, head_at_request, status, requested_by, created_at, updated_at, closed_at) VALUES ($1, $2, $3, 'apply', 1, $4, 0, 'executed', $5, now(), now(), now())", requestID, databaseID, migrationID, contentHash, userID)
	exec(insertRequest, openRequestID, databaseID, pendingID, "apply", 1, contentHash, nil, userID)
	exec(insertExecution, executionID, databaseID, 1, migrationID, requestID, "apply", contentHash, "succeeded", userID)
	exec(insertExecution, dryRunID, databaseID, 2, pendingID, nil, "dry_run", contentHash, "queued", userID)
	exec(insertExecution, otherDryRunID, databaseID, 3, pendingID, nil, "dry_run", contentHash, "queued", userID)
	exec(insertExecution, unknownOutcomeID, databaseID, 4, pendingID, nil, "dry_run", contentHash, "outcome_unknown", userID)
	exec(insertHistory, databaseID, 1, executionID, migrationID, "apply", contentHash, userID)
	exec(insertApproval, approvalID, openRequestID, groupID, userID, "approved", nil)
	exec("INSERT INTO outbox_messages (id, subject, payload) VALUES ($1, 'migrations.jobs.' || $2::text, '{}')", unknownID, databaseID)

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
		{"leader outside group", "UPDATE groups SET leader_user_id=$2 WHERE id=$1", "23503", []any{groupID, viewerID}},
		{"leader membership removal", "DELETE FROM group_memberships WHERE group_id=$1 AND user_id=$2", "23503", []any{groupID, userID}},
		{"blank migration title", "UPDATE migrations SET title='  ' WHERE id=$1", "23514", []any{migrationID}},
		{"long migration description", "UPDATE migrations SET description=$2 WHERE id=$1", "23514", []any{migrationID, strings.Repeat("a", 2001)}},
		{"migration unlock", "UPDATE migrations SET status='pending', locked_at=NULL, locked_revision=NULL WHERE id=$1", "23514", []any{migrationID}},
		{"revision after lock", "UPDATE migrations SET current_revision=2 WHERE id=$1", "23514", []any{migrationID}},
		{"applied without lock", "UPDATE migrations SET status='applied' WHERE id=$1", "23514", []any{pendingID}},
		{"duplicate version", "UPDATE migrations SET status='applied', version=1, locked_revision=1, locked_at=now() WHERE id=$1", "23505", []any{pendingID}},
		{"revision update", "UPDATE migration_revisions SET up_sql='SELECT 1' WHERE migration_id=$1", "23514", []any{migrationID}},
		{"revision delete", "DELETE FROM migration_revisions WHERE migration_id=$1", "23514", []any{migrationID}},
		{"down and irreversible", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", "data is lost", "normal", nil, contentHash, userID}},
		{"neither down nor irreversible", insertRevision, "23514", []any{pendingID, 2, nil, nil, "normal", nil, contentHash, userID}},
		{"batch mode without config", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", nil, "batch", nil, contentHash, userID}},
		{"batch config on normal mode", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", nil, "normal", validBatch, contentHash, userID}},
		{"batch config extra key", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", nil, "batch", `{"schema":"public","table":"items","key_column":"id","batch_size":10,"pause_ms":0,"x":1}`, contentHash, userID}},
		{"batch size zero", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", nil, "batch", `{"schema":"public","table":"items","key_column":"id","batch_size":0,"pause_ms":0}`, contentHash, userID}},
		{"batch pause as text", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", nil, "batch", `{"schema":"public","table":"items","key_column":"id","batch_size":10,"pause_ms":"0"}`, contentHash, userID}},
		{"short content hash", insertRevision, "23514", []any{pendingID, 2, "UPDATE items SET note = NULL", nil, "normal", nil, []byte{1}, userID}},
		{"second open request", insertRequest, "23505", []any{unknownID, databaseID, pendingID, "apply", 1, contentHash, nil, userID}},
		{"resume request", insertRequest, "23514", []any{unknownID, databaseID, migrationID, "resume", 1, contentHash, nil, userID}},
		{"apply with target execution", insertRequest, "23514", []any{unknownID, databaseID, migrationID, "apply", 1, contentHash, executionID, userID}},
		{"request for unknown revision", insertRequest, "23503", []any{unknownID, databaseID, migrationID, "rollback", 9, contentHash, nil, userID}},
		{"closed request without closed_at", "UPDATE change_requests SET status='cancelled' WHERE id=$1", "23514", []any{openRequestID}},
		{"dry-run with request", insertExecution, "23514", []any{unknownID, databaseID, 5, pendingID, openRequestID, "dry_run", contentHash, "queued", userID}},
		{"apply without request", insertExecution, "23514", []any{unknownID, databaseID, 5, pendingID, nil, "apply", contentHash, "queued", userID}},
		{"duplicate execution number", insertExecution, "23505", []any{unknownID, databaseID, 1, pendingID, nil, "dry_run", contentHash, "queued", userID}},
		{"two running executions", "UPDATE migration_executions SET status='running' WHERE id IN ($1, $2)", "23505", []any{dryRunID, otherDryRunID}},
		{"terminal execution change", "UPDATE migration_executions SET error_code='X' WHERE id=$1", "23514", []any{executionID}},
		{"execution input change", "UPDATE migration_executions SET number=99 WHERE id=$1", "23514", []any{dryRunID}},
		{"attempt above max", "UPDATE migration_executions SET attempt=4 WHERE id=$1", "23514", []any{dryRunID}},
		{"unknown outcome restart", "UPDATE migration_executions SET status='running' WHERE id=$1", "23514", []any{unknownOutcomeID}},
		{"empty batch record", "INSERT INTO migration_batches (execution_id, batch_no, direction, cursor_from, cursor_to, rows, committed_at) VALUES ($1, 1, 'up', 0, 10, 0, now())", "23514", []any{executionID}},
		{"history update", "UPDATE migration_history SET rows=5", "23514", nil},
		{"history delete", "DELETE FROM migration_history", "23514", nil},
		{"rollback history without reverted execution", insertHistory, "23514", []any{databaseID, 2, dryRunID, migrationID, "rollback", contentHash, userID}},
		{"second active approval", insertApproval, "23505", []any{unknownID, openRequestID, groupID, userID, "approved", nil}},
		{"rejection without comment", insertApproval, "23514", []any{unknownID, openRequestID, unknownID, userID, "rejected", nil}},
		{"approval decision change", "UPDATE change_request_approvals SET decision='rejected', comment='no' WHERE id=$1", "23514", []any{approvalID}},
		{"outbox headers not object", "INSERT INTO outbox_messages (id, subject, payload, headers) VALUES ($1, 'migrations.jobs.x', '{}', '[]')", "23514", []any{viewerID}},
	} {
		t.Run(tt.name, func(t *testing.T) { expectSQLState(t, tt.code, tt.query, tt.args...) })
	}
	t.Run("batch revision", func(t *testing.T) {
		exec(insertRevision, pendingID, 2, "UPDATE items SET note = NULL", nil, "batch", validBatch, contentHash, userID)
	})
	t.Run("reconcile resolves unknown outcome", func(t *testing.T) {
		exec("UPDATE migration_executions SET status='queued', reconcile_requested=true WHERE id=$1", unknownOutcomeID)
	})
	t.Run("approval is dismissed once", func(t *testing.T) {
		exec("UPDATE change_request_approvals SET dismissed_at=now() WHERE id=$1", approvalID)
		expectSQLState(t, "23514", "UPDATE change_request_approvals SET dismissed_at=now() + interval '1 second' WHERE id=$1", approvalID)
		exec(insertApproval, unknownID, openRequestID, groupID, userID, "approved", nil)
	})
	t.Run("group deletion cleans memberships, grants and approvals", func(t *testing.T) {
		exec("DELETE FROM groups WHERE id=$1", groupID)
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM group_memberships WHERE group_id=$1)+(SELECT count(*) FROM group_database_access WHERE group_id=$1)+(SELECT count(*) FROM change_request_approvals WHERE group_id=$1)", groupID).Scan(&count); err != nil || count != 0 {
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
