package access_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const viewGroup = "20000000-0000-4000-8000-000000000001"
const editGroup = "20000000-0000-4000-8000-000000000002"

func accessDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_METADATA_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_METADATA_DATABASE_URL is required in CI")
		}
		t.Skip("TEST_METADATA_DATABASE_URL is not set")
	}
	admin, err := platform.OpenMetadataDB(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "test_access_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error("test schema cleanup failed")
		}
	})
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
	for i, id := range []string{testkit.UserAdmin, testkit.UserView, testkit.UserEdit, testkit.UserNone} {
		_, err := db.Exec(`INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,'$argon2id$test',$3)`, id, []string{"admin", "viewer", "editor", "outsider"}[i], i == 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{testkit.DBPG, testkit.DBSQLite} {
		if _, err := db.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,'target','postgres','\x00')`, id); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{viewGroup, editGroup} {
		if _, err := db.Exec(`INSERT INTO groups(id,name) VALUES($1,$2)`, id, []string{"readers", "writers"}[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO group_database_access(group_id,database_id,level) VALUES($1,$2,$3)`, id, testkit.DBPG, []string{"view", "edit"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{viewGroup, testkit.UserView}, {viewGroup, testkit.UserEdit}, {editGroup, testkit.UserEdit}} {
		if _, err := db.Exec(`INSERT INTO group_memberships(group_id,user_id) VALUES($1,$2)`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	if code == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var domain *contracts.DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestPolicyAccessMatrix(t *testing.T) {
	db := accessDB(t)
	p := access.NewPolicy(db)
	for _, tc := range []struct {
		user               string
		level              contracts.AccessLevel
		view, edit, manage string
	}{
		{testkit.UserAdmin, contracts.AccessNone, "NOT_FOUND", "NOT_FOUND", ""},
		{testkit.UserView, contracts.AccessView, "", "FORBIDDEN", "FORBIDDEN"},
		{testkit.UserEdit, contracts.AccessEdit, "", "", "FORBIDDEN"},
		{testkit.UserNone, contracts.AccessNone, "NOT_FOUND", "NOT_FOUND", "FORBIDDEN"},
	} {
		t.Run(tc.user, func(t *testing.T) {
			level, err := p.Effective(t.Context(), tc.user, testkit.DBPG)
			if err != nil || level != tc.level {
				t.Fatal("incorrect effective access", level, err)
			}
			principal := contracts.Principal{UserID: tc.user, IsPlatformAdmin: true}
			requireCode(t, p.Require(t.Context(), principal, contracts.ViewDatabase, testkit.DBPG), tc.view)
			requireCode(t, p.Require(t.Context(), principal, contracts.EditDatabase, testkit.DBPG), tc.edit)
			requireCode(t, p.Require(t.Context(), principal, contracts.ManagePlatform, ""), tc.manage)
		})
	}
	requireCode(t, p.Require(t.Context(), contracts.Principal{}, contracts.ViewDatabase, testkit.DBPG), "UNAUTHENTICATED")
	admin := contracts.Principal{UserID: testkit.UserAdmin}
	requireCode(t, p.Require(t.Context(), admin, contracts.ManagePlatform, ""), "")
	requireCode(t, p.Require(t.Context(), admin, contracts.ManagePlatform, testkit.DBPG), "FORBIDDEN")
	requireCode(t, p.Require(t.Context(), admin, contracts.Action("unknown"), ""), "FORBIDDEN")
	for _, id := range []string{testkit.DBSQLite, testkit.DBPGDesired, ""} {
		requireCode(t, p.Require(t.Context(), contracts.Principal{UserID: testkit.UserEdit}, contracts.ViewDatabase, id), "NOT_FOUND")
	}
}

func TestPolicyReadsChangesImmediately(t *testing.T) {
	db := accessDB(t)
	p := access.NewPolicy(db)
	for _, step := range []struct {
		query string
		args  []any
		user  string
		want  contracts.AccessLevel
	}{
		{`DELETE FROM group_database_access WHERE group_id=$1`, []any{editGroup}, testkit.UserEdit, contracts.AccessView},
		{`DELETE FROM group_memberships WHERE user_id=$1`, []any{testkit.UserEdit}, testkit.UserEdit, contracts.AccessNone},
		{`UPDATE users SET active=false WHERE id=$1`, []any{testkit.UserView}, testkit.UserView, contracts.AccessNone},
		{`INSERT INTO group_memberships(group_id,user_id) VALUES($1,$2)`, []any{viewGroup, testkit.UserAdmin}, testkit.UserAdmin, contracts.AccessView},
		{`UPDATE database_connections SET active=false WHERE id=$1`, []any{testkit.DBPG}, testkit.UserAdmin, contracts.AccessNone},
	} {
		if _, err := db.Exec(step.query, step.args...); err != nil {
			t.Fatal(err)
		}
		level, err := p.Effective(t.Context(), step.user, testkit.DBPG)
		if err != nil || level != step.want {
			t.Fatal("stale effective access", level, err)
		}
	}
	requireCode(t, p.Require(t.Context(), contracts.Principal{UserID: testkit.UserView}, contracts.ViewDatabase, testkit.DBPG), "UNAUTHENTICATED")
	if _, err := db.Exec(`UPDATE users SET is_platform_admin=false WHERE id=$1`, testkit.UserAdmin); err != nil {
		t.Fatal(err)
	}
	requireCode(t, p.Require(t.Context(), contracts.Principal{UserID: testkit.UserAdmin, IsPlatformAdmin: true}, contracts.ManagePlatform, ""), "FORBIDDEN")
	level, err := p.Effective(t.Context(), "10000000-0000-4000-8000-000000000099", testkit.DBPG)
	if err != nil || level != contracts.AccessNone {
		t.Fatal("unknown user received access")
	}
}

func TestPolicyFailuresDenyAccess(t *testing.T) {
	db := accessDB(t)
	p := access.NewPolicy(db)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := p.Effective(ctx, testkit.UserEdit, testkit.DBPG)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	level, err := p.Effective(t.Context(), testkit.UserEdit, testkit.DBPG)
	requireCode(t, err, "INTERNAL_ERROR")
	if level != contracts.AccessNone || strings.Contains(err.Error(), "database is closed") {
		t.Fatal("failure leaked details or granted access")
	}
	requireCode(t, p.Require(t.Context(), contracts.Principal{UserID: testkit.UserAdmin}, contracts.ManagePlatform, ""), "INTERNAL_ERROR")
}

func createSession(t *testing.T, db *sql.DB, store *auth.SessionStore, user string) auth.Session {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	session, err := store.CreateInTx(t.Context(), tx, user)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestDisableCommitsAndRollsBackWithConnection(t *testing.T) {
	db := accessDB(t)
	clock := testkit.NewClock(time.Now())
	store, err := auth.NewSessionStore(db, clock, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := access.NewDatabaseAccessLifecycle(store)
	reader := createSession(t, db, store, testkit.UserView)
	writer := createSession(t, db, store, testkit.UserEdit)
	other := createSession(t, db, store, testkit.UserNone)
	prelogin := createSession(t, db, store, "")
	clock.Advance(15 * time.Minute)
	rotated, err := store.Resolve(t.Context(), writer.Token)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE database_connections SET active=false WHERE id=$1`, testkit.DBPG); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Disable(t.Context(), tx, testkit.DBPG); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{reader.Token, writer.Token, rotated.Token} {
		if _, err := store.Resolve(t.Context(), token); err != nil {
			t.Fatal("rollback revoked a session", err)
		}
	}
	reg := connections.NewRegistry(db, nil, connections.RegistryOptions{})
	svc := connections.NewService(db, nil, reg, nil, lifecycle, nil, connections.ServiceOptions{})
	if err := svc.Deactivate(t.Context(), testkit.DBPG); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{reader.Token, writer.Token, rotated.Token} {
		_, err := store.Resolve(t.Context(), token)
		requireCode(t, err, "UNAUTHENTICATED")
	}
	if _, err := store.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("unrelated session revoked")
	}
	var revoked bool
	if err := db.QueryRow(`SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1`, prelogin.ID).Scan(&revoked); err != nil || revoked {
		t.Fatal("prelogin session was revoked")
	}
	level, err := access.NewPolicy(db).Effective(t.Context(), testkit.UserEdit, testkit.DBPG)
	if err != nil || level != contracts.AccessNone {
		t.Fatal("disabled target retained access")
	}
}

func TestPolicyWithSessionMiddleware(t *testing.T) {
	db := accessDB(t)
	store, err := auth.NewSessionStore(db, platform.SystemClock{}, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	h, err := auth.NewHTTPHandler(store, "http://localhost:8080", false)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.NewPolicy(db)
	for _, tc := range []struct {
		user   string
		action contracts.Action
		csrf   bool
		status int
	}{
		{testkit.UserView, contracts.ViewDatabase, true, 204},
		{testkit.UserView, contracts.EditDatabase, true, 403},
		{testkit.UserEdit, contracts.EditDatabase, true, 204},
		{testkit.UserNone, contracts.ViewDatabase, true, 404},
		{testkit.UserAdmin, contracts.ViewDatabase, true, 404},
		{testkit.UserEdit, contracts.EditDatabase, false, 403},
		{"", contracts.ViewDatabase, true, 401},
	} {
		session := createSession(t, db, store, tc.user)
		reached := false
		handler := h.RequireSession(h.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, _ := auth.PrincipalFromContext(r.Context())
			if err := policy.Require(r.Context(), principal, tc.action, testkit.DBPG); err != nil {
				_ = platform.WriteError(w, r, err)
				return
			}
			reached = true
			w.WriteHeader(http.StatusNoContent)
		})))
		r := httptest.NewRequest("POST", "/protected", nil)
		r.AddCookie(&http.Cookie{Name: "session", Value: session.Token})
		r.Header.Set("Origin", "http://localhost:8080")
		if tc.csrf {
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || reached != (tc.status == 204) {
			t.Fatalf("expected %d, got %d", tc.status, w.Code)
		}
	}
}

func TestDisableFailureRollsBackConnection(t *testing.T) {
	db := accessDB(t)
	store, err := auth.NewSessionStore(db, platform.SystemClock{}, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	session := createSession(t, db, store, testkit.UserEdit)
	_, err = db.Exec(`CREATE FUNCTION reject_revocation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'private revocation failure'; END; $$;
		CREATE TRIGGER reject_revocation BEFORE UPDATE ON sessions FOR EACH ROW EXECUTE FUNCTION reject_revocation()`)
	if err != nil {
		t.Fatal(err)
	}
	reg := connections.NewRegistry(db, nil, connections.RegistryOptions{})
	svc := connections.NewService(db, nil, reg, nil, access.NewDatabaseAccessLifecycle(store), nil, connections.ServiceOptions{})
	err = svc.Deactivate(t.Context(), testkit.DBPG)
	requireCode(t, err, "INTERNAL_ERROR")
	if strings.Contains(err.Error(), "private") {
		t.Fatal("internal failure leaked")
	}
	var active, revoked bool
	if err := db.QueryRow(`SELECT active FROM database_connections WHERE id=$1`, testkit.DBPG).Scan(&active); err != nil || !active {
		t.Fatal("failed revocation disabled connection")
	}
	if err := db.QueryRow(`SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1`, session.ID).Scan(&revoked); err != nil || revoked {
		t.Fatal("failed revocation changed session")
	}
}
