package review_test

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/review"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	userLead    = "10000000-0000-4000-8000-000000000005"
	userViewer  = "10000000-0000-4000-8000-000000000002"
	userEditor  = "10000000-0000-4000-8000-000000000003"
	groupView   = "20000000-0000-4000-8000-000000000001"
	groupEdit   = "20000000-0000-4000-8000-000000000002"
	groupOps    = "20000000-0000-4000-8000-000000000003"
	dbShared    = "30000000-0000-4000-8000-000000000001"
	dbOps       = "30000000-0000-4000-8000-000000000005"
	dbUngranted = "30000000-0000-4000-8000-000000000002"
	migrationID = "40000000-0000-4000-8000-000000000001"
	requestID   = "60000000-0000-4000-8000-000000000001"
)

func reviewDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	admin, err := platform.OpenDatabase(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "test_review_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`) })
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateMetadata(t.Context(), db, metadata.Files); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(query, err)
		}
	}
	for id, name := range map[string]string{userLead: "lead", userViewer: "viewer", userEditor: "editor"} {
		exec(`INSERT INTO users(id, username, password_hash) VALUES($1, $2, '$argon2id$test')`, id, name)
	}
	for _, id := range []string{dbShared, dbOps, dbUngranted} {
		exec(`INSERT INTO database_connections(id, name, driver, config_encrypted) VALUES($1, $2, 'postgres', '\x00')`, id, "target-"+id[len(id)-1:])
	}
	for id, name := range map[string]string{groupView: "readers", groupEdit: "writers", groupOps: "ops"} {
		exec(`INSERT INTO groups(id, name) VALUES($1, $2)`, id, name)
	}
	for _, m := range [][2]string{{groupView, userViewer}, {groupView, userEditor}, {groupEdit, userEditor}, {groupEdit, userLead}, {groupOps, userLead}} {
		exec(`INSERT INTO group_memberships(group_id, user_id) VALUES($1, $2)`, m[0], m[1])
	}
	for group, leader := range map[string]string{groupView: userViewer, groupEdit: userLead, groupOps: userLead} {
		exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, group, leader)
	}
	for _, g := range [][3]string{{groupView, dbShared, "view"}, {groupEdit, dbShared, "edit"}, {groupOps, dbOps, "edit"}} {
		exec(`INSERT INTO group_database_access(group_id, database_id, level) VALUES($1, $2, $3)`, g[0], g[1], g[2])
	}
	return db
}

// newRequest stores a pending apply request for revision 1 or 2 of a migration on databaseID.
func newRequest(t *testing.T, db *sql.DB, databaseID string, revision int64) contracts.ReviewSubject {
	t.Helper()
	hash := sha256.Sum256([]byte("SELECT 1"))
	for _, step := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO migrations(id, database_id, title, current_revision, created_by, updated_by, created_at, updated_at)
			VALUES($1, $2, 'Add column', $3, $4, $4, now(), now())`, []any{migrationID, databaseID, revision, userEditor}},
		{`INSERT INTO migration_revisions(migration_id, revision, up_sql, down_sql, mode, content_hash, created_by, created_at)
			SELECT $1, r, 'SELECT 1', 'SELECT 1', 'normal', $2, $3, now() FROM generate_series(1, $4::bigint) r`, []any{migrationID, hash[:], userEditor, revision}},
		{`INSERT INTO change_requests(id, database_id, migration_id, action, migration_revision, content_hash, head_at_request, requested_by, created_at, updated_at)
			VALUES($1, $2, $3, 'apply', $4, $5, 0, $6, now(), now())`, []any{requestID, databaseID, migrationID, revision, hash[:], userEditor}},
	} {
		if _, err := db.Exec(step.query, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	return contracts.ReviewSubject{RequestID: requestID, DatabaseID: databaseID, MigrationID: migrationID, Action: "apply",
		Revision: revision, ContentHash: hash[:], RequestedBy: userEditor, Status: "pending_review"}
}

func decide(t *testing.T, db *sql.DB, group, approver, decision string, revision int64) {
	t.Helper()
	var comment any
	if decision == "rejected" {
		comment = "Tambahkan indeks dulu."
	}
	if _, err := db.Exec(`INSERT INTO change_request_approvals(id, request_id, group_id, approver_id, decision, comment, migration_revision, created_at)
		VALUES(gen_random_uuid(), $1, $2, $3, $4, $5, $6, now() - interval '1 minute')`, requestID, group, approver, decision, comment, revision); err != nil {
		t.Fatal(err)
	}
}

func service(db *sql.DB) *review.Service {
	return review.NewService(db, nil, &testkit.AuditRecorder{}, testkit.NewClock(time.Now()))
}

func evaluate(t *testing.T, db *sql.DB, subject contracts.ReviewSubject) contracts.ReviewState {
	t.Helper()
	state, err := service(db).Evaluate(t.Context(), db, subject)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func decisions(state contracts.ReviewState) string {
	parts := []string{}
	for _, r := range state.Required {
		parts = append(parts, r.GroupName+"="+r.Decision)
	}
	return strings.Join(parts, ",")
}

func TestEvaluateRequiresEveryGrantedGroupLeader(t *testing.T) {
	db := reviewDB(t)
	subject := newRequest(t, db, dbShared, 1)
	state := evaluate(t, db, subject)
	if state.Status != "pending_review" || decisions(state) != "readers=pending,writers=pending" {
		t.Fatal("view and edit groups must both be required", state)
	}
	if *state.Required[0].LeaderID != userViewer || *state.Required[0].LeaderName != "viewer" || *state.Required[1].LeaderID != userLead {
		t.Fatal("required approvals must name the current leaders", state.Required)
	}
	decide(t, db, groupView, userViewer, "approved", 1)
	if state := evaluate(t, db, subject); state.Status != "pending_review" || decisions(state) != "readers=approved,writers=pending" {
		t.Fatal("one of two approvals is not enough", state)
	}
	decide(t, db, groupEdit, userLead, "approved", 1)
	state = evaluate(t, db, subject)
	if state.Status != "approved" || state.Required[1].DecidedAt == nil || state.Required[1].DecidedAt.Location() != time.UTC {
		t.Fatal("all leaders approved", state)
	}
	// A group granted after the approvals becomes required at once.
	if _, err := db.Exec(`INSERT INTO group_database_access(group_id, database_id, level) VALUES($1, $2, 'view')`, groupOps, dbShared); err != nil {
		t.Fatal(err)
	}
	if state := evaluate(t, db, subject); state.Status != "pending_review" || decisions(state) != "ops=pending,readers=approved,writers=approved" {
		t.Fatal("a newly granted group must approve too", state)
	}
}

func TestEvaluateSingleTeamDatabase(t *testing.T) {
	db := reviewDB(t)
	subject := newRequest(t, db, dbOps, 1)
	decide(t, db, groupOps, userLead, "approved", 1)
	if state := evaluate(t, db, subject); state.Status != "approved" || decisions(state) != "ops=approved" {
		t.Fatal("only the ops leader is required", state)
	}
}

func TestEvaluateRejectionWins(t *testing.T) {
	db := reviewDB(t)
	subject := newRequest(t, db, dbShared, 1)
	decide(t, db, groupView, userViewer, "approved", 1)
	decide(t, db, groupEdit, userLead, "rejected", 1)
	state := evaluate(t, db, subject)
	if state.Status != "rejected" || *state.Required[1].Comment != "Tambahkan indeks dulu." {
		t.Fatal("a rejection must decide the request", state)
	}
}

func TestEvaluateIgnoresFormerLeadersAndOldRevisions(t *testing.T) {
	db := reviewDB(t)
	subject := newRequest(t, db, dbShared, 2)
	decide(t, db, groupView, userViewer, "approved", 2)
	decide(t, db, groupEdit, userLead, "approved", 1)
	if state := evaluate(t, db, subject); state.Status != "pending_review" || decisions(state) != "readers=approved,writers=pending" {
		t.Fatal("an approval of an older revision must not count", state)
	}
	if _, err := db.Exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, groupView, userEditor); err != nil {
		t.Fatal(err)
	}
	state := evaluate(t, db, subject)
	if decisions(state) != "readers=pending,writers=pending" || *state.Required[0].LeaderID != userEditor {
		t.Fatal("a former leader's approval must not count", state)
	}
}

func TestEvaluateWithoutRequiredGroupsStaysPending(t *testing.T) {
	db := reviewDB(t)
	subject := newRequest(t, db, dbUngranted, 1)
	if state := evaluate(t, db, subject); state.Status != "pending_review" || len(state.Required) != 0 || state.Required == nil {
		t.Fatal("a request nobody can approve must stay pending", state)
	}
}

func TestDismissApprovalsWithinCallerTransaction(t *testing.T) {
	db := reviewDB(t)
	subject := newRequest(t, db, dbShared, 1)
	decide(t, db, groupView, userViewer, "approved", 1)
	decide(t, db, groupEdit, userLead, "approved", 1)
	svc := service(db)
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.DismissApprovals(t.Context(), tx, requestID); err != nil {
			t.Fatal(err)
		}
		if state, err := svc.Evaluate(t.Context(), tx, subject); err != nil || state.Status != "pending_review" {
			t.Fatal("dismissed approvals still count inside the transaction", state, err)
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		want := map[bool]string{false: "approved", true: "pending_review"}[commit]
		if state := evaluate(t, db, subject); state.Status != want {
			t.Fatal("dismissal must follow the caller's transaction", commit, state)
		}
	}
	var dismissed int
	if err := db.QueryRow(`SELECT count(*) FROM change_request_approvals WHERE dismissed_at IS NOT NULL`).Scan(&dismissed); err != nil || dismissed != 2 {
		t.Fatal("dismissal must keep the rows", dismissed, err)
	}
}
