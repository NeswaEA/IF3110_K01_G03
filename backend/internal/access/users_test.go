package access_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

const userPassword = "example password 123"
const missingUser = "10000000-0000-4000-8000-000000000099"

var adminPrincipal = contracts.Principal{UserID: testkit.UserAdmin, IsPlatformAdmin: true}

type userFixture struct {
	db       *sql.DB
	clock    *testkit.Clock
	sessions *auth.SessionStore
	users    *access.UserService
}

func newUserFixture(t *testing.T) userFixture {
	t.Helper()
	db := accessDB(t)
	clock := testkit.NewClock(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if _, err := db.Exec(`UPDATE users SET created_at=$1, updated_at=$1`, clock.Now()); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessionStore(db, clock, auth.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	return userFixture{db: db, clock: clock, sessions: sessions,
		users: access.NewUserService(db, sessions, access.NewPolicy(db), clock, auth.NormalizeUsername, (auth.PasswordHasher{}).Hash)}
}

func flag(value bool) *bool { return &value }

func TestUsersCreateStopsOnNormalizationError(t *testing.T) {
	want := &contracts.DomainError{Code: "INVALID_REQUEST", Fields: map[string]string{"username": "Invalid username."}}
	users := access.NewUserService(nil, nil, testkit.NewFixtureAccessChecker(), nil, func(username string) (string, error) {
		if username != "invalid username" {
			t.Fatal("normalizer did not receive the original username")
		}
		return "", want
	}, func(context.Context, string) (string, error) {
		t.Fatal("password hashing ran after normalization failed")
		return "", nil
	})
	_, err := users.Create(t.Context(), adminPrincipal, access.CreateUserInput{Username: "invalid username", Password: userPassword})
	if err != want {
		t.Fatal("normalization error was changed", err)
	}
}

func TestUsersPasswordHashErrorsStopBeforeStorage(t *testing.T) {
	for _, operation := range []string{"create", "reset"} {
		t.Run(operation, func(t *testing.T) {
			ctx := t.Context()
			want := &contracts.DomainError{Code: "RATE_LIMITED", Message: "Password hashing is busy."}
			users := access.NewUserService(nil, nil, testkit.NewFixtureAccessChecker(), nil, auth.NormalizeUsername, func(got context.Context, password string) (string, error) {
				if got != ctx || password != userPassword {
					t.Fatal("hasher did not receive the request context and original password")
				}
				return "", want
			})
			var err error
			if operation == "create" {
				_, err = users.Create(ctx, adminPrincipal, access.CreateUserInput{Username: "valid-user", Password: userPassword})
			} else {
				err = users.ResetPassword(ctx, adminPrincipal, testkit.UserView, userPassword)
			}
			if err != want {
				t.Fatal("password hashing error was changed", err)
			}
		})
	}
}

func TestUsersCreateAndList(t *testing.T) {
	f := newUserFixture(t)
	user, err := f.users.Create(t.Context(), adminPrincipal, access.CreateUserInput{Username: "  New.User  ", Password: userPassword})
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "new.user" || user.IsPlatformAdmin || !user.Active || user.ID == "" ||
		!user.CreatedAt.Equal(f.clock.Now()) || !user.UpdatedAt.Equal(user.CreatedAt) {
		t.Fatal("incorrect created user", user)
	}
	var hash string
	if err := f.db.QueryRow(`SELECT password_hash FROM users WHERE id=$1`, user.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if match, err := (auth.PasswordHasher{}).Verify(t.Context(), userPassword, hash); err != nil || !match {
		t.Fatal("password was not stored as a valid hash")
	}
	_, err = f.users.Create(t.Context(), adminPrincipal, access.CreateUserInput{Username: "NEW.USER", Password: userPassword})
	requireCode(t, err, "ALREADY_EXISTS")
	admin, err := f.users.Create(t.Context(), adminPrincipal, access.CreateUserInput{Username: "z-admin", Password: userPassword, IsPlatformAdmin: true})
	if err != nil || !admin.IsPlatformAdmin || !admin.Active {
		t.Fatal("admin creation failed", err)
	}
	level, err := access.NewPolicy(f.db).Effective(t.Context(), admin.ID, testkit.DBPG)
	if err != nil || level != contracts.AccessNone {
		t.Fatal("admin was granted target access automatically")
	}
	users, more, err := f.users.List(t.Context(), adminPrincipal, 2, 1)
	if err != nil || !more || len(users) != 2 || users[0].Username != "editor" || users[1].Username != "new.user" {
		t.Fatal("incorrect ordered page", users, more, err)
	}
	users, more, err = f.users.List(t.Context(), adminPrincipal, 6, 0)
	if err != nil || more || len(users) != 6 {
		t.Fatal("exact final page has_more is incorrect", err)
	}
	users, more, err = f.users.List(t.Context(), adminPrincipal, 50, 100000)
	if err != nil || more || users == nil || len(users) != 0 {
		t.Fatal("empty page is not an empty collection", err)
	}
}

func TestUsersValidateInputAndMissingRecords(t *testing.T) {
	f := newUserFixture(t)
	for _, input := range []access.CreateUserInput{
		{Username: "ab", Password: userPassword},
		{Username: "bad'username", Password: userPassword},
		{Username: "valid", Password: "short"},
		{Username: "valid", Password: strings.Repeat("p", 129)},
	} {
		_, err := f.users.Create(t.Context(), adminPrincipal, input)
		requireCode(t, err, "INVALID_REQUEST")
	}
	for _, page := range [][2]int{{0, 0}, {201, 0}, {50, -1}, {50, 100001}} {
		_, _, err := f.users.List(t.Context(), adminPrincipal, page[0], page[1])
		requireCode(t, err, "INVALID_REQUEST")
	}
	_, err := f.users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{})
	requireCode(t, err, "INVALID_REQUEST")
	for _, id := range []string{"not-a-uuid", missingUser} {
		code := "INVALID_REQUEST"
		if id == missingUser {
			code = "NOT_FOUND"
		}
		_, err := f.users.Update(t.Context(), adminPrincipal, id, access.UpdateUserInput{Active: flag(false)})
		requireCode(t, err, code)
		requireCode(t, f.users.ResetPassword(t.Context(), adminPrincipal, id, userPassword), code)
	}
	requireCode(t, f.users.ResetPassword(t.Context(), adminPrincipal, testkit.UserView, "short"), "INVALID_REQUEST")
}

func TestUsersDeactivateAndReactivate(t *testing.T) {
	f := newUserFixture(t)
	first := createSession(t, f.db, f.sessions, testkit.UserView)
	second := createSession(t, f.db, f.sessions, testkit.UserView)
	other := createSession(t, f.db, f.sessions, testkit.UserEdit)
	prelogin := createSession(t, f.db, f.sessions, "")
	f.clock.Advance(15 * time.Minute)
	rotated, err := f.sessions.Resolve(t.Context(), first.Token)
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{Active: flag(false)})
	if err != nil || user.Active {
		t.Fatal("deactivation failed", err)
	}
	for _, token := range []string{first.Token, second.Token, rotated.Token} {
		_, err := f.sessions.Resolve(t.Context(), token)
		requireCode(t, err, "UNAUTHENTICATED")
	}
	if _, err := f.sessions.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("unrelated user session was revoked", err)
	}
	var revoked bool
	if err := f.db.QueryRow(`SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1`, prelogin.ID).Scan(&revoked); err != nil || revoked {
		t.Fatal("anonymous session was revoked", err)
	}
	var memberships int
	if err := f.db.QueryRow(`SELECT count(*) FROM group_memberships WHERE user_id=$1`, testkit.UserView).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatal("deactivation removed membership or user history", err)
	}
	user, err = f.users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{Active: flag(true)})
	if err != nil || !user.Active {
		t.Fatal("reactivation failed", err)
	}
	_, err = f.sessions.Resolve(t.Context(), rotated.Token)
	requireCode(t, err, "UNAUTHENTICATED")
}

func TestUsersPromotionRotatesAndDemotionRevokes(t *testing.T) {
	f := newUserFixture(t)
	session := createSession(t, f.db, f.sessions, testkit.UserView)
	admin := createSession(t, f.db, f.sessions, testkit.UserAdmin)
	f.clock.Advance(15 * time.Minute)
	current, err := f.sessions.Resolve(t.Context(), session.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{IsPlatformAdmin: flag(true)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.sessions.Resolve(t.Context(), session.Token)
	requireCode(t, err, "UNAUTHENTICATED")
	elevated, err := f.sessions.Resolve(t.Context(), current.Token)
	if err != nil || elevated.Token == "" || elevated.Token == current.Token || !elevated.Principal.IsPlatformAdmin || !elevated.AbsoluteExpiresAt.Equal(session.AbsoluteExpiresAt) {
		t.Fatal("promotion did not rotate before granting admin privileges", err)
	}
	_, err = f.users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{IsPlatformAdmin: flag(false)})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{current.Token, elevated.Token} {
		_, err := f.sessions.Resolve(t.Context(), token)
		requireCode(t, err, "UNAUTHENTICATED")
	}
	if _, err := f.sessions.Resolve(t.Context(), admin.Token); err != nil {
		t.Fatal("unrelated admin was revoked", err)
	}
}

func TestUsersNoopAndLastAdmin(t *testing.T) {
	f := newUserFixture(t)
	session := createSession(t, f.db, f.sessions, testkit.UserAdmin)
	f.clock.Advance(time.Minute)
	user, err := f.users.Update(t.Context(), adminPrincipal, testkit.UserAdmin, access.UpdateUserInput{Active: flag(true), IsPlatformAdmin: flag(true)})
	if err != nil || !user.UpdatedAt.Equal(f.clock.Now().Add(-time.Minute)) {
		t.Fatal("no-op changed the user timestamp", err)
	}
	for _, input := range []access.UpdateUserInput{
		{Active: flag(false)}, {IsPlatformAdmin: flag(false)}, {Active: flag(false), IsPlatformAdmin: flag(false)},
	} {
		_, err := f.users.Update(t.Context(), adminPrincipal, testkit.UserAdmin, input)
		requireCode(t, err, "INVALID_REQUEST")
	}
	resolved, err := f.sessions.Resolve(t.Context(), session.Token)
	if err != nil || resolved.Token != "" || !resolved.Principal.IsPlatformAdmin {
		t.Fatal("rejected update changed the admin session", err)
	}
}

func TestUsersConcurrentLastAdminChanges(t *testing.T) {
	for _, mode := range []string{"deactivate", "demote", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			f := newUserFixture(t)
			if _, err := f.db.Exec(`UPDATE users SET is_platform_admin=true WHERE id=$1`, testkit.UserEdit); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i, id := range []string{testkit.UserAdmin, testkit.UserEdit} {
				wg.Go(func() {
					<-start
					input := access.UpdateUserInput{Active: flag(false)}
					if mode == "demote" || (mode == "mixed" && i == 1) {
						input = access.UpdateUserInput{IsPlatformAdmin: flag(false)}
					}
					_, err := f.users.Update(ctx, adminPrincipal, id, input)
					results <- err
				})
			}
			close(start)
			wg.Wait()
			close(results)
			succeeded := 0
			for err := range results {
				if err == nil {
					succeeded++
				} else {
					var domain *contracts.DomainError
					if !errors.As(err, &domain) || (domain.Code != "INVALID_REQUEST" && domain.Code != "UNAUTHENTICATED" && domain.Code != "FORBIDDEN") {
						t.Fatal("unexpected concurrent admin change error", err)
					}
				}
			}
			var remaining int
			if err := f.db.QueryRow(`SELECT count(*) FROM users WHERE active AND is_platform_admin`).Scan(&remaining); err != nil || remaining != 1 || succeeded != 1 {
				t.Fatal("concurrent requests removed all admins", remaining, succeeded, err)
			}
		})
	}
}

type failingUserSessions struct{ sessions *auth.SessionStore }

func (f failingUserSessions) RevokeUsers(ctx context.Context, tx *sql.Tx, ids []string) error {
	if err := f.sessions.RevokeUsers(ctx, tx, ids); err != nil {
		return err
	}
	return errors.New("session invalidation failed")
}

func (f failingUserSessions) RequireRotation(ctx context.Context, tx *sql.Tx, ids []string) error {
	if err := f.sessions.RequireRotation(ctx, tx, ids); err != nil {
		return err
	}
	return errors.New("session rotation failed")
}

func TestUsersSessionFailureRollsBackMetadata(t *testing.T) {
	f := newUserFixture(t)
	broken := access.NewUserService(f.db, failingUserSessions{f.sessions}, access.NewPolicy(f.db), f.clock, auth.NormalizeUsername, (auth.PasswordHasher{}).Hash)
	session := createSession(t, f.db, f.sessions, testkit.UserView)
	for _, input := range []access.UpdateUserInput{{Active: flag(false)}, {IsPlatformAdmin: flag(true)}} {
		_, err := broken.Update(t.Context(), adminPrincipal, testkit.UserView, input)
		requireCode(t, err, "INTERNAL_ERROR")
		var active, admin bool
		if err := f.db.QueryRow(`SELECT active, is_platform_admin FROM users WHERE id=$1`, testkit.UserView).Scan(&active, &admin); err != nil || !active || admin {
			t.Fatal("user mutation survived rollback", err)
		}
		resolved, err := f.sessions.Resolve(t.Context(), session.Token)
		if err != nil || resolved.Token != "" {
			t.Fatal("session mutation survived rollback", err)
		}
	}
	requireCode(t, broken.ResetPassword(t.Context(), adminPrincipal, testkit.UserView, userPassword), "INTERNAL_ERROR")
	var hash string
	if err := f.db.QueryRow(`SELECT password_hash FROM users WHERE id=$1`, testkit.UserView).Scan(&hash); err != nil || hash != "$argon2id$test" {
		t.Fatal("password reset survived rollback", err)
	}
	if _, err := f.sessions.Resolve(t.Context(), session.Token); err != nil {
		t.Fatal("password-reset revocation survived rollback", err)
	}
}

func TestUsersFailuresPreserveCause(t *testing.T) {
	f := newUserFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := f.users.List(ctx, adminPrincipal, 50, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause was lost", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.users.List(t.Context(), adminPrincipal, 50, 0)
	requireCode(t, err, "INTERNAL_ERROR")
	if strings.Contains(err.Error(), "database is closed") {
		t.Fatal("storage failure leaked details")
	}
}
