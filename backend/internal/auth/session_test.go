package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	metadata "github.com/NeswaEA/IF3110_K01_G03/backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const sessionUser = "10000000-0000-4000-8000-000000000003"
const otherSessionUser = "10000000-0000-4000-8000-000000000002"

type sessionFixture struct {
	db    *sql.DB
	clock *testkit.Clock
	store *auth.SessionStore
}

func newSessionFixture(t *testing.T) sessionFixture {
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
	schema := "test_session_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal("create test schema failed")
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
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	if err := platform.MigrateMetadata(t.Context(), db, metadata.Files); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO users (id, username, password_hash) VALUES
		($1, 'editor', '$argon2id$test'), ($2, 'viewer', '$argon2id$test')`, sessionUser, otherSessionUser)
	if err != nil {
		t.Fatal("create test users failed")
	}
	clock := testkit.NewClock(time.Date(2026, 10, 21, 0, 0, 0, 123456789, time.UTC))
	store, err := auth.NewSessionStore(db, clock, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	return sessionFixture{db: db, clock: clock, store: store}
}

func (f sessionFixture) create(t *testing.T, userID string) auth.Session {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	session, err := f.store.CreateInTx(t.Context(), tx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return session
}

func (f sessionFixture) change(t *testing.T, commit bool, fn func(*sql.Tx) error) {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		t.Fatal(err)
	}
	if commit {
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func requireSessionError(t *testing.T, err error, code string) {
	t.Helper()
	var domain *contracts.DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestSessionStoreValidation(t *testing.T) {
	clock := testkit.NewClock(time.Now())
	options := auth.DefaultSessionOptions()
	if _, err := auth.NewSessionStore(nil, clock, options); err == nil {
		t.Fatal("nil database accepted")
	}
	if _, err := auth.NewSessionStore(&sql.DB{}, nil, options); err == nil {
		t.Fatal("nil clock accepted")
	}
	for _, bad := range []auth.SessionOptions{
		{}, {IdleTimeout: -1},
		{IdleTimeout: time.Nanosecond, AbsoluteTimeout: time.Hour, RotationInterval: time.Minute, GracePeriod: time.Second},
	} {
		if _, err := auth.NewSessionStore(&sql.DB{}, clock, bad); err == nil {
			t.Fatal("invalid duration accepted")
		}
	}
	store, err := auth.NewSessionStore(&sql.DB{}, clock, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "invalid", strings.Repeat("A", 42), strings.Repeat("A", 44), strings.Repeat("!", 43), strings.Repeat("A", 42) + "B"} {
		_, err := store.Resolve(t.Context(), token)
		requireSessionError(t, err, "UNAUTHENTICATED")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Resolve(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was ignored")
	}
	_, err = store.CreateInTx(t.Context(), nil, "")
	requireSessionError(t, err, "INTERNAL_ERROR")
	requireSessionError(t, store.RevokeInTx(t.Context(), nil, ""), "INTERNAL_ERROR")
	requireSessionError(t, store.RevokeUsers(t.Context(), nil, nil), "INTERNAL_ERROR")
	requireSessionError(t, store.RequireRotation(t.Context(), nil, nil), "INTERNAL_ERROR")
}

func TestSessionCreationAndPrivacy(t *testing.T) {
	f := newSessionFixture(t)
	prelogin := f.create(t, "")
	loggedIn := f.create(t, sessionUser)
	if prelogin.Principal != (contracts.Principal{}) || loggedIn.Principal.UserID != sessionUser ||
		loggedIn.Principal.Username != "editor" || loggedIn.Principal.SessionID != loggedIn.ID {
		t.Fatal("incorrect session identity")
	}
	now := f.clock.Now().Truncate(time.Microsecond)
	if !prelogin.AbsoluteExpiresAt.Equal(now.Add(10*time.Minute)) || !loggedIn.AbsoluteExpiresAt.Equal(now.Add(8*time.Hour)) {
		t.Fatal("incorrect session lifetime")
	}
	if prelogin.ID == loggedIn.ID || prelogin.Token == loggedIn.Token || prelogin.CSRFToken == loggedIn.CSRFToken {
		t.Fatal("session secrets were reused")
	}
	for _, session := range []auth.Session{prelogin, loggedIn} {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(session.Token)
		if err != nil || len(raw) != 32 {
			t.Fatal("invalid opaque token")
		}
		csrf, err := base64.RawURLEncoding.Strict().DecodeString(session.CSRFToken)
		if err != nil || len(csrf) != 32 || bytes.Equal(raw, csrf) {
			t.Fatal("invalid CSRF secret")
		}
		var stored []byte
		if err := f.db.QueryRow(`SELECT token_hash FROM session_tokens WHERE session_id=$1`, session.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if !bytes.Equal(stored, hash[:]) || bytes.Equal(stored, raw) {
			t.Fatal("opaque token was not hashed before storage")
		}
		body, err := json.Marshal(session)
		if err != nil || string(body) != "{}" {
			t.Fatal("internal session fields exposed through JSON")
		}
		resolved, err := f.store.Resolve(t.Context(), session.Token)
		if err != nil || resolved.Token != "" || resolved.Principal != session.Principal || resolved.CSRFToken != session.CSRFToken {
			t.Fatal("session lookup did not preserve identity and CSRF")
		}
	}
	_, err := f.store.Resolve(t.Context(), base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	requireSessionError(t, err, "UNAUTHENTICATED")
}

func TestSessionExpiryBoundaries(t *testing.T) {
	for _, kind := range []string{"prelogin", "idle", "absolute"} {
		t.Run(kind, func(t *testing.T) {
			f := newSessionFixture(t)
			userID := sessionUser
			if kind == "prelogin" {
				userID = ""
			}
			session := f.create(t, userID)
			original := session
			var before time.Time
			switch kind {
			case "prelogin":
				f.clock.Advance(10 * time.Minute)
			case "idle":
				f.clock.Advance(30 * time.Minute)
			case "absolute":
				for i := 0; i < 31; i++ {
					f.clock.Advance(15 * time.Minute)
					next, err := f.store.Resolve(t.Context(), session.Token)
					if err != nil || next.Token == "" || !next.AbsoluteExpiresAt.Equal(original.AbsoluteExpiresAt) {
						t.Fatal("active session failed or deadline changed")
					}
					session = next
				}
				f.clock.Advance(15 * time.Minute)
			}
			if err := f.db.QueryRow(`SELECT last_seen_at FROM sessions WHERE id=$1`, session.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, err := f.store.Resolve(t.Context(), session.Token)
			requireSessionError(t, err, "UNAUTHENTICATED")
			var after time.Time
			if err := f.db.QueryRow(`SELECT last_seen_at FROM sessions WHERE id=$1`, session.ID).Scan(&after); err != nil || !after.Equal(before) {
				t.Fatal("expired request extended activity")
			}
		})
	}
}

func TestSessionConcurrentRotationAndGrace(t *testing.T) {
	f := newSessionFixture(t)
	session := f.create(t, sessionUser)
	f.clock.Advance(15 * time.Minute)
	var wg sync.WaitGroup
	results := make(chan auth.Session, 12)
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			<-start
			resolved, err := f.store.Resolve(t.Context(), session.Token)
			if err != nil {
				t.Error(err)
				return
			}
			results <- resolved
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var rotated auth.Session
	winners, requests := 0, 0
	for result := range results {
		requests++
		if result.Token != "" {
			winners++
			rotated = result
		}
	}
	if winners != 1 || requests != 12 || rotated.Token == session.Token || rotated.CSRFToken != session.CSRFToken || !rotated.AbsoluteExpiresAt.Equal(session.AbsoluteExpiresAt) {
		t.Fatal("concurrent rotation did not produce exactly one new token")
	}
	var generation, current, total int
	if err := f.db.QueryRow(`SELECT generation FROM sessions WHERE id=$1`, session.ID).Scan(&generation); err != nil || generation != 2 {
		t.Fatal("session generation advanced more than once")
	}
	if err := f.db.QueryRow(`SELECT count(*) FILTER (WHERE is_current), count(*) FROM session_tokens WHERE session_id=$1`, session.ID).Scan(&current, &total); err != nil || current != 1 || total != 2 {
		t.Fatal("incorrect token count after rotation")
	}
	f.clock.Advance(30*time.Second - time.Microsecond)
	old, err := f.store.Resolve(t.Context(), session.Token)
	if err != nil || old.Token != "" {
		t.Fatal("old token failed during grace or was reissued")
	}
	f.clock.Advance(time.Microsecond)
	_, err = f.store.Resolve(t.Context(), session.Token)
	requireSessionError(t, err, "UNAUTHENTICATED")
	if _, err := f.store.Resolve(t.Context(), rotated.Token); err != nil {
		t.Fatal("new token failed after grace")
	}
}

func TestSessionGraceCannotExtendAbsoluteExpiry(t *testing.T) {
	f := newSessionFixture(t)
	options := auth.DefaultSessionOptions()
	options.AbsoluteTimeout = 15*time.Minute + 10*time.Second
	var err error
	f.store, err = auth.NewSessionStore(f.db, f.clock, options)
	if err != nil {
		t.Fatal(err)
	}
	session := f.create(t, sessionUser)
	f.clock.Advance(15 * time.Minute)
	rotated, err := f.store.Resolve(t.Context(), session.Token)
	if err != nil || rotated.Token == "" {
		t.Fatal("rotation failed")
	}
	var validUntil time.Time
	if err := f.db.QueryRow(`SELECT valid_until FROM session_tokens WHERE session_id=$1 AND NOT is_current`, session.ID).Scan(&validUntil); err != nil || !validUntil.Equal(session.AbsoluteExpiresAt) {
		t.Fatal("grace exceeded absolute expiry")
	}
	f.clock.Advance(10 * time.Second)
	for _, token := range []string{session.Token, rotated.Token} {
		_, err := f.store.Resolve(t.Context(), token)
		requireSessionError(t, err, "UNAUTHENTICATED")
	}
}

func TestSessionRotationRequiredAndTransactionalRevocation(t *testing.T) {
	f := newSessionFixture(t)
	first := f.create(t, sessionUser)
	second := f.create(t, sessionUser)
	other := f.create(t, otherSessionUser)
	for _, commit := range []bool{false, true} {
		f.change(t, commit, func(tx *sql.Tx) error {
			return f.store.RequireRotation(t.Context(), tx, []string{sessionUser, sessionUser})
		})
		resolved, err := f.store.Resolve(t.Context(), first.Token)
		if err != nil || (resolved.Token != "") != commit {
			t.Fatal("rotation flag did not follow caller transaction")
		}
		if commit {
			first = resolved
		}
	}
	f.change(t, true, func(tx *sql.Tx) error {
		return f.store.RequireRotation(t.Context(), tx, []string{sessionUser})
	})
	var priorToken string
	secondRotated, err := f.store.Resolve(t.Context(), second.Token)
	if err != nil || secondRotated.Token == "" {
		t.Fatal("forced rotation failed")
	}
	priorToken = second.Token
	f.change(t, true, func(tx *sql.Tx) error {
		return f.store.RequireRotation(t.Context(), tx, []string{sessionUser})
	})
	_, err = f.store.Resolve(t.Context(), priorToken)
	requireSessionError(t, err, "UNAUTHENTICATED")
	for _, commit := range []bool{false, true} {
		f.change(t, commit, func(tx *sql.Tx) error {
			return f.store.RevokeUsers(t.Context(), tx, []string{sessionUser, sessionUser})
		})
		for _, token := range []string{first.Token, secondRotated.Token} {
			resolved, err := f.store.Resolve(t.Context(), token)
			if commit {
				requireSessionError(t, err, "UNAUTHENTICATED")
			} else if err != nil {
				t.Fatal("rollback revoked a session")
			} else if resolved.Token != "" {
				if token == first.Token {
					first = resolved
				} else {
					secondRotated = resolved
				}
			}
		}
	}
	_, err = f.store.Resolve(t.Context(), priorToken)
	requireSessionError(t, err, "UNAUTHENTICATED")
	if _, err := f.store.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("unrelated user session was revoked")
	}
	f.change(t, true, func(tx *sql.Tx) error {
		if err := f.store.RevokeUsers(t.Context(), tx, nil); err != nil {
			return err
		}
		return f.store.RequireRotation(t.Context(), tx, nil)
	})
}

func TestSessionReplacementAndRollback(t *testing.T) {
	f := newSessionFixture(t)
	prelogin := f.create(t, "")
	var loggedIn auth.Session
	for _, commit := range []bool{false, true} {
		f.change(t, commit, func(tx *sql.Tx) error {
			var err error
			loggedIn, err = f.store.CreateInTx(t.Context(), tx, sessionUser)
			if err != nil {
				return err
			}
			return f.store.RevokeInTx(t.Context(), tx, prelogin.ID)
		})
		_, oldErr := f.store.Resolve(t.Context(), prelogin.Token)
		_, newErr := f.store.Resolve(t.Context(), loggedIn.Token)
		if commit {
			requireSessionError(t, oldErr, "UNAUTHENTICATED")
			if newErr != nil || loggedIn.CSRFToken == prelogin.CSRFToken {
				t.Fatal("authenticated replacement was not created")
			}
		} else {
			if oldErr != nil {
				t.Fatal("rollback revoked prelogin session")
			}
			requireSessionError(t, newErr, "UNAUTHENTICATED")
		}
	}
	f.clock.Advance(15 * time.Minute)
	rotated, err := f.store.Resolve(t.Context(), loggedIn.Token)
	if err != nil || rotated.Token == "" {
		t.Fatal("rotation failed")
	}
	f.change(t, true, func(tx *sql.Tx) error {
		if err := f.store.RevokeInTx(t.Context(), tx, loggedIn.ID); err != nil {
			return err
		}
		return f.store.RevokeInTx(t.Context(), tx, loggedIn.ID)
	})
	for _, token := range []string{loggedIn.Token, rotated.Token} {
		_, err := f.store.Resolve(t.Context(), token)
		requireSessionError(t, err, "UNAUTHENTICATED")
	}
}

func TestSessionReadsCurrentUserAndMonotonicActivity(t *testing.T) {
	f := newSessionFixture(t)
	session := f.create(t, sessionUser)
	f.clock.Advance(time.Minute)
	if _, err := f.store.Resolve(t.Context(), session.Token); err != nil {
		t.Fatal(err)
	}
	expected := f.clock.Now().Truncate(time.Microsecond)
	f.clock.Advance(-time.Minute)
	if _, err := f.db.Exec(`UPDATE users SET is_platform_admin=true, username='updated' WHERE id=$1`, sessionUser); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.store.Resolve(t.Context(), session.Token)
	if err != nil || !resolved.Principal.IsPlatformAdmin || resolved.Principal.Username != "updated" {
		t.Fatal("session used stale user data")
	}
	var lastSeen time.Time
	if err := f.db.QueryRow(`SELECT last_seen_at FROM sessions WHERE id=$1`, session.ID).Scan(&lastSeen); err != nil || !lastSeen.Equal(expected) {
		t.Fatal("activity moved backwards")
	}
	if _, err := f.db.Exec(`UPDATE users SET active=false WHERE id=$1`, sessionUser); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.Resolve(t.Context(), session.Token)
	requireSessionError(t, err, "UNAUTHENTICATED")
	f.change(t, false, func(tx *sql.Tx) error {
		_, err := f.store.CreateInTx(t.Context(), tx, sessionUser)
		requireSessionError(t, err, "UNAUTHENTICATED")
		_, err = f.store.CreateInTx(t.Context(), tx, "10000000-0000-4000-8000-000000000099")
		requireSessionError(t, err, "UNAUTHENTICATED")
		return nil
	})
}

func TestSessionRotationFailureRollsBack(t *testing.T) {
	f := newSessionFixture(t)
	session := f.create(t, sessionUser)
	_, err := f.db.Exec(`CREATE FUNCTION reject_rotation() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'private storage failure'; END; $$;
		CREATE TRIGGER reject_rotation BEFORE INSERT ON session_tokens FOR EACH ROW EXECUTE FUNCTION reject_rotation()`)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(15 * time.Minute)
	_, err = f.store.Resolve(t.Context(), session.Token)
	requireSessionError(t, err, "INTERNAL_ERROR")
	if strings.Contains(err.Error(), "private storage failure") || strings.Contains(err.Error(), session.Token) {
		t.Fatal("session failure exposed private data")
	}
	var generation, current, total int
	if err := f.db.QueryRow(`SELECT generation FROM sessions WHERE id=$1`, session.ID).Scan(&generation); err != nil || generation != 1 {
		t.Fatal("failed rotation advanced generation")
	}
	if err := f.db.QueryRow(`SELECT count(*) FILTER (WHERE is_current), count(*) FROM session_tokens WHERE session_id=$1`, session.ID).Scan(&current, &total); err != nil || current != 1 || total != 1 {
		t.Fatal("failed rotation invalidated the existing token")
	}
	if _, err := f.db.Exec(`DROP TRIGGER reject_rotation ON session_tokens`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Resolve(t.Context(), session.Token); err != nil {
		t.Fatal("retry after failed rotation was rejected")
	}
}
