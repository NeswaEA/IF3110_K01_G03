package access_test

import (
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestAdminChangesAreAuditedAfterCommit(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	actor := contracts.Principal{UserID: testkit.UserAdmin, Username: "admin", IsPlatformAdmin: true}
	if _, err := svc.SetLeader(t.Context(), actor, viewGroup, testkit.UserView); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetLeader(t.Context(), actor, viewGroup, testkit.UserView); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetGrant(t.Context(), actor, viewGroup, testkit.DBSQLite, contracts.AccessView); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetGrant(t.Context(), actor, viewGroup, testkit.DBSQLite, contracts.AccessView); err != nil {
		t.Fatal(err)
	}
	if _, err := f.users.Update(t.Context(), actor, testkit.UserNone, access.UpdateUserInput{Active: flag(false)}); err != nil {
		t.Fatal(err)
	}
	if err := f.users.ResetPassword(t.Context(), actor, testkit.UserNone, userPassword); err != nil {
		t.Fatal(err)
	}
	events := f.audit.Events()
	if len(events) != 4 {
		t.Fatal("no-op changes must not be audited", events)
	}
	for i, want := range []struct{ action, resource string }{
		{"group.leader_changed", viewGroup}, {"grant.changed", viewGroup}, {"user.changed", testkit.UserNone}, {"user.changed", testkit.UserNone},
	} {
		e := events[i]
		if e.Action != want.action || e.ResourceID != want.resource || *e.ActorID != testkit.UserAdmin || *e.ActorName != "admin" {
			t.Fatal("incorrect audit event", i, e)
		}
	}
	if events[0].Details["leader_id"] != testkit.UserView || events[0].Details["previous_leader_id"] != "" {
		t.Fatal("leader change details", events[0].Details)
	}
	if *events[1].DatabaseID != testkit.DBSQLite || events[1].Details["previous_level"] != "none" || events[1].Details["level"] != "view" {
		t.Fatal("grant change details", events[1])
	}
	if events[3].Details["change"] != "password_reset" || len(events[3].Details) != 1 {
		t.Fatal("password reset must not carry the password", events[3].Details)
	}
}

func TestRolledBackAdminChangesAreNotAudited(t *testing.T) {
	f := newUserFixture(t)
	leadGroups(t, f.db)
	recorder := &testkit.AuditRecorder{}
	groups := access.NewGroupService(f.db, failingUserSessions{f.sessions}, access.NewPolicy(f.db), f.clock, recorder)
	users := access.NewUserService(f.db, failingUserSessions{f.sessions}, access.NewPolicy(f.db), f.clock, recorder, nil, nil)
	_, err := groups.SetGrant(t.Context(), adminPrincipal, viewGroup, testkit.DBSQLite, contracts.AccessEdit)
	requireCode(t, err, "INTERNAL_ERROR")
	_, err = users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{Active: flag(false)})
	requireCode(t, err, "INTERNAL_ERROR")
	_, err = groups.SetGrant(t.Context(), adminPrincipal, editGroup, testkit.DBSQLite, contracts.AccessView)
	requireCode(t, err, "INTERNAL_ERROR")
	if events := recorder.Events(); len(events) != 0 {
		t.Fatal("rolled back change was audited", events)
	}
}
