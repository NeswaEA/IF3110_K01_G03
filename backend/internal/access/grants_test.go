package access_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestGrantsListAndValidation(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	grant, err := svc.SetGrant(t.Context(), viewGroup, testkit.DBSQLite, contracts.AccessEdit)
	if err != nil || grant.DatabaseID != testkit.DBSQLite || grant.Level != contracts.AccessEdit {
		t.Fatal("grant creation failed", grant, err)
	}
	grants, more, err := svc.Grants(t.Context(), viewGroup, 1, 0)
	if err != nil || !more || len(grants) != 1 || grants[0].DatabaseID != testkit.DBPG || grants[0].Level != contracts.AccessView {
		t.Fatal("incorrect first grant page", grants, more, err)
	}
	grants, more, err = svc.Grants(t.Context(), viewGroup, 1, 1)
	if err != nil || more || len(grants) != 1 || grants[0] != grant {
		t.Fatal("incorrect final grant page", grants, more, err)
	}
	grants, more, err = svc.Grants(t.Context(), viewGroup, 50, 100000)
	if err != nil || more || grants == nil || len(grants) != 0 {
		t.Fatal("empty grant page must be an empty array", err)
	}
	policy := access.NewPolicy(f.db)
	for databaseID, want := range map[string]contracts.AccessLevel{testkit.DBPG: contracts.AccessView, testkit.DBSQLite: contracts.AccessEdit} {
		level, err := policy.Effective(t.Context(), testkit.UserView, databaseID)
		if err != nil || level != want {
			t.Fatal("grant level leaked across databases", databaseID, level, err)
		}
	}
	level, err := policy.Effective(t.Context(), testkit.UserAdmin, testkit.DBSQLite)
	if err != nil || level != contracts.AccessNone {
		t.Fatal("platform admin received target access without membership", err)
	}
	empty, err := svc.Create(t.Context(), "empty grants")
	if err != nil {
		t.Fatal(err)
	}
	grants, more, err = svc.Grants(t.Context(), empty.ID, 50, 0)
	if err != nil || more || grants == nil || len(grants) != 0 {
		t.Fatal("empty group must have no grants", err)
	}
	for _, level := range []contracts.AccessLevel{contracts.AccessNone, "", "VIEW", "admin"} {
		_, err := svc.SetGrant(t.Context(), viewGroup, testkit.DBPG, level)
		requireCode(t, err, "INVALID_REQUEST")
	}
	for _, page := range [][2]int{{0, 0}, {201, 0}, {50, -1}, {50, 100001}} {
		_, _, err := svc.Grants(t.Context(), viewGroup, page[0], page[1])
		requireCode(t, err, "INVALID_REQUEST")
	}
	for _, id := range []string{"bad-id", missingUser} {
		code := "NOT_FOUND"
		if id == "bad-id" {
			code = "INVALID_REQUEST"
		}
		_, _, err := svc.Grants(t.Context(), id, 50, 0)
		requireCode(t, err, code)
		_, err = svc.SetGrant(t.Context(), id, testkit.DBPG, contracts.AccessView)
		requireCode(t, err, code)
		_, err = svc.SetGrant(t.Context(), viewGroup, id, contracts.AccessView)
		requireCode(t, err, code)
		requireCode(t, svc.RemoveGrant(t.Context(), id, testkit.DBPG), code)
		requireCode(t, svc.RemoveGrant(t.Context(), viewGroup, id), code)
	}
}

func TestGrantIncreaseRotatesAllMembersAndNoopPreservesSessions(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	reader := createSession(t, f.db, f.sessions, testkit.UserView)
	f.clock.Advance(15 * time.Minute)
	current, err := f.sessions.Resolve(t.Context(), reader.Token)
	if err != nil {
		t.Fatal(err)
	}
	second := createSession(t, f.db, f.sessions, testkit.UserView)
	writer := createSession(t, f.db, f.sessions, testkit.UserEdit)
	other := createSession(t, f.db, f.sessions, testkit.UserNone)
	admin := createSession(t, f.db, f.sessions, testkit.UserAdmin)
	prelogin := createSession(t, f.db, f.sessions, "")
	if _, err := svc.SetGrant(t.Context(), viewGroup, testkit.DBPG, contracts.AccessEdit); err != nil {
		t.Fatal(err)
	}
	_, err = f.sessions.Resolve(t.Context(), reader.Token)
	requireCode(t, err, "UNAUTHENTICATED")
	tokens := []string{}
	for token, deadline := range map[string]time.Time{
		current.Token: reader.AbsoluteExpiresAt, second.Token: second.AbsoluteExpiresAt, writer.Token: writer.AbsoluteExpiresAt,
	} {
		rotated, err := f.sessions.Resolve(t.Context(), token)
		if err != nil || rotated.Token == "" || !rotated.AbsoluteExpiresAt.Equal(deadline) {
			t.Fatal("grant increase did not rotate every member session without extending its deadline", err)
		}
		tokens = append(tokens, rotated.Token)
	}
	for _, token := range []string{other.Token, admin.Token, prelogin.Token} {
		resolved, err := f.sessions.Resolve(t.Context(), token)
		if err != nil || resolved.Token != "" {
			t.Fatal("grant change touched a non-member session", err)
		}
	}
	var before, after time.Time
	if err := f.db.QueryRow(`SELECT updated_at FROM group_database_access WHERE group_id=$1 AND database_id=$2`, viewGroup, testkit.DBPG).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	if _, err := svc.SetGrant(t.Context(), viewGroup, testkit.DBPG, contracts.AccessEdit); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT updated_at FROM group_database_access WHERE group_id=$1 AND database_id=$2`, viewGroup, testkit.DBPG).Scan(&after); err != nil || !after.Equal(before) {
		t.Fatal("duplicate grant changed updated_at", err)
	}
	for _, token := range tokens {
		resolved, err := f.sessions.Resolve(t.Context(), token)
		if err != nil || resolved.Token != "" {
			t.Fatal("duplicate grant invalidated a session", err)
		}
	}
}

func TestGrantDowngradeAndRemovalRevokeMembers(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	reader := createSession(t, f.db, f.sessions, testkit.UserView)
	writer := createSession(t, f.db, f.sessions, testkit.UserEdit)
	other := createSession(t, f.db, f.sessions, testkit.UserNone)
	if _, err := svc.SetGrant(t.Context(), editGroup, testkit.DBPG, contracts.AccessView); err != nil {
		t.Fatal(err)
	}
	_, err := f.sessions.Resolve(t.Context(), writer.Token)
	requireCode(t, err, "UNAUTHENTICATED")
	if resolved, err := f.sessions.Resolve(t.Context(), reader.Token); err != nil || resolved.Token != "" {
		t.Fatal("downgrade affected a non-member", err)
	}
	f.clock.Advance(15 * time.Minute)
	rotated, err := f.sessions.Resolve(t.Context(), reader.Token)
	if err != nil {
		t.Fatal(err)
	}
	freshWriter := createSession(t, f.db, f.sessions, testkit.UserEdit)
	if err := svc.RemoveGrant(t.Context(), viewGroup, testkit.DBPG); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{reader.Token, rotated.Token, freshWriter.Token} {
		_, err := f.sessions.Resolve(t.Context(), token)
		requireCode(t, err, "UNAUTHENTICATED")
	}
	policy := access.NewPolicy(f.db)
	for userID, want := range map[string]contracts.AccessLevel{testkit.UserView: contracts.AccessNone, testkit.UserEdit: contracts.AccessView} {
		level, err := policy.Effective(t.Context(), userID, testkit.DBPG)
		if err != nil || level != want {
			t.Fatal("remaining group grants were not respected", userID, level, err)
		}
	}
	if _, err := f.sessions.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("removal revoked an unrelated user's session", err)
	}
	freshReader := createSession(t, f.db, f.sessions, testkit.UserView)
	if err := svc.RemoveGrant(t.Context(), viewGroup, testkit.DBPG); err != nil {
		t.Fatal("repeated grant removal failed", err)
	}
	if _, err := f.sessions.Resolve(t.Context(), freshReader.Token); err != nil {
		t.Fatal("removing a missing grant revoked a fresh session", err)
	}
}

func TestGrantSessionFailuresRollBackChanges(t *testing.T) {
	f := newUserFixture(t)
	broken := access.NewGroupService(f.db, failingUserSessions{f.sessions}, f.clock)
	reader := createSession(t, f.db, f.sessions, testkit.UserView)
	writer := createSession(t, f.db, f.sessions, testkit.UserEdit)
	for _, change := range []struct {
		group, database string
		level           contracts.AccessLevel
	}{
		{viewGroup, testkit.DBPG, contracts.AccessEdit},
		{editGroup, testkit.DBPG, contracts.AccessView},
		{viewGroup, testkit.DBSQLite, contracts.AccessView},
	} {
		_, err := broken.SetGrant(t.Context(), change.group, change.database, change.level)
		requireCode(t, err, "INTERNAL_ERROR")
	}
	requireCode(t, broken.RemoveGrant(t.Context(), viewGroup, testkit.DBPG), "INTERNAL_ERROR")
	for group, want := range map[string]contracts.AccessLevel{viewGroup: contracts.AccessView, editGroup: contracts.AccessEdit} {
		grants, more, err := groupService(f).Grants(t.Context(), group, 50, 0)
		if err != nil || more || len(grants) != 1 || grants[0].DatabaseID != testkit.DBPG || grants[0].Level != want {
			t.Fatal("grant change survived rollback", group, grants, err)
		}
	}
	for _, token := range []string{reader.Token, writer.Token} {
		resolved, err := f.sessions.Resolve(t.Context(), token)
		if err != nil || resolved.Token != "" {
			t.Fatal("session change survived grant rollback", err)
		}
	}
	if _, err := broken.SetGrant(t.Context(), viewGroup, testkit.DBPG, contracts.AccessView); err != nil {
		t.Fatal("no-op grant should not call the invalidator", err)
	}
	if err := broken.RemoveGrant(t.Context(), viewGroup, testkit.DBSQLite); err != nil {
		t.Fatal("missing grant should not call the invalidator", err)
	}
}

func TestGrantsOnInactiveConnections(t *testing.T) {
	f := newUserFixture(t)
	if _, err := f.db.Exec(`UPDATE database_connections SET active=false WHERE id=$1`, testkit.DBSQLite); err != nil {
		t.Fatal(err)
	}
	svc := groupService(f)
	if _, err := svc.SetGrant(t.Context(), viewGroup, testkit.DBSQLite, contracts.AccessEdit); err != nil {
		t.Fatal("admin should be able to manage an inactive connection's metadata", err)
	}
	grants, _, err := svc.Grants(t.Context(), viewGroup, 50, 0)
	if err != nil || len(grants) != 2 {
		t.Fatal("inactive connection grant was hidden from administration", err)
	}
	level, err := access.NewPolicy(f.db).Effective(t.Context(), testkit.UserView, testkit.DBSQLite)
	if err != nil || level != contracts.AccessNone {
		t.Fatal("grant made an inactive connection accessible", err)
	}
	if err := svc.RemoveGrant(t.Context(), viewGroup, testkit.DBSQLite); err != nil {
		t.Fatal("inactive connection grant could not be removed", err)
	}
}

func TestGrantConcurrentChanges(t *testing.T) {
	for _, mode := range []string{"add member", "delete group", "disable connection"} {
		t.Run(mode, func(t *testing.T) {
			f := newUserFixture(t)
			svc := groupService(f)
			group, err := svc.Create(t.Context(), "concurrent grant")
			if err != nil {
				t.Fatal(err)
			}
			if mode != "add member" {
				if err := svc.AddMember(t.Context(), group.ID, testkit.UserNone); err != nil {
					t.Fatal(err)
				}
			}
			session := createSession(t, f.db, f.sessions, testkit.UserNone)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			start := make(chan struct{})
			granted, changed := make(chan error, 1), make(chan error, 1)
			go func() {
				<-start
				_, err := svc.SetGrant(ctx, group.ID, testkit.DBPG, contracts.AccessEdit)
				granted <- err
			}()
			go func() {
				<-start
				switch mode {
				case "add member":
					changed <- svc.AddMember(ctx, group.ID, testkit.UserNone)
				case "delete group":
					changed <- svc.Delete(ctx, group.ID)
				case "disable connection":
					registry := connections.NewRegistry(f.db, nil, connections.RegistryOptions{})
					defer registry.Close()
					connectionsService := connections.NewService(f.db, nil, registry, nil, access.NewDatabaseAccessLifecycle(f.sessions), nil, connections.ServiceOptions{})
					changed <- connectionsService.Deactivate(ctx, testkit.DBPG)
				}
			}()
			close(start)
			grantErr, changeErr := <-granted, <-changed
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if mode == "delete group" && grantErr != nil {
				requireCode(t, grantErr, "NOT_FOUND")
			} else if grantErr != nil {
				t.Fatal(grantErr)
			}
			level, err := access.NewPolicy(f.db).Effective(t.Context(), testkit.UserNone, testkit.DBPG)
			want := contracts.AccessNone
			if mode == "add member" {
				want = contracts.AccessEdit
			}
			if err != nil || level != want {
				t.Fatal("concurrent mutation left incorrect access", level, err)
			}
			resolved, err := f.sessions.Resolve(t.Context(), session.Token)
			if mode == "add member" {
				if err != nil || resolved.Token == "" {
					t.Fatal("concurrent membership addition did not rotate the session", err)
				}
			} else if mode == "delete group" {
				requireCode(t, err, "UNAUTHENTICATED")
			} else if err != nil {
				requireCode(t, err, "UNAUTHENTICATED")
			} else if resolved.Token == "" {
				t.Fatal("concurrent grant change did not rotate or revoke the session")
			}
		})
	}
}

func TestGrantFailuresPreserveCause(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := svc.Grants(ctx, viewGroup, 50, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause was lost", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SetGrant(t.Context(), viewGroup, testkit.DBPG, contracts.AccessView)
	requireCode(t, err, "INTERNAL_ERROR")
}
