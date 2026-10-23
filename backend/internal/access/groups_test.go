package access_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func groupService(f userFixture) *access.GroupService {
	return access.NewGroupService(f.db, f.sessions, access.NewPolicy(f.db), f.clock)
}

func TestGroupGetByID(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	group, err := svc.Get(t.Context(), adminPrincipal, viewGroup)
	if err != nil || group.ID != viewGroup || group.Name != "readers" || group.CreatedAt.IsZero() || group.UpdatedAt.IsZero() {
		t.Fatal("group lookup returned incorrect data", group, err)
	}
	if _, err := svc.Update(t.Context(), adminPrincipal, viewGroup, "renamed readers"); err != nil {
		t.Fatal(err)
	}
	group, err = svc.Get(t.Context(), adminPrincipal, viewGroup)
	if err != nil || group.ID != viewGroup || group.Name != "renamed readers" {
		t.Fatal("group lookup depended on list order", group, err)
	}
	for _, id := range []string{"", "invalid", "20000000-0000-4000-8000-00000000000A"} {
		_, err := svc.Get(t.Context(), adminPrincipal, id)
		requireCode(t, err, "INVALID_REQUEST")
	}
	_, err = svc.Get(t.Context(), adminPrincipal, missingUser)
	requireCode(t, err, "NOT_FOUND")
}

func TestGroupsCreateUpdateAndList(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	groups, more, err := svc.List(t.Context(), adminPrincipal, 1, 0)
	if err != nil || !more || len(groups) != 1 || groups[0].Name != "readers" {
		t.Fatal("incorrect first group page", groups, more, err)
	}
	groups, more, err = svc.List(t.Context(), adminPrincipal, 1, 1)
	if err != nil || more || len(groups) != 1 || groups[0].Name != "writers" {
		t.Fatal("incorrect final group page", groups, more, err)
	}
	groups, more, err = svc.List(t.Context(), adminPrincipal, 50, 100000)
	if err != nil || more || groups == nil || len(groups) != 0 {
		t.Fatal("empty group page must be an empty collection", err)
	}
	group, err := svc.Create(t.Context(), adminPrincipal, "  Operators\t")
	if err != nil || group.ID == "" || group.Name != "Operators" || !group.CreatedAt.Equal(f.clock.Now()) || !group.UpdatedAt.Equal(group.CreatedAt) {
		t.Fatal("incorrect group creation", group, err)
	}
	_, err = svc.Create(t.Context(), adminPrincipal, "Operators")
	requireCode(t, err, "ALREADY_EXISTS")
	if _, err := svc.Create(t.Context(), adminPrincipal, "operators"); err != nil {
		t.Fatal("group names must remain case-sensitive", err)
	}
	f.clock.Advance(time.Minute)
	updated, err := svc.Update(t.Context(), adminPrincipal, group.ID, " Team ")
	if err != nil || updated.ID != group.ID || updated.Name != "Team" || !updated.CreatedAt.Equal(group.CreatedAt) || !updated.UpdatedAt.Equal(f.clock.Now()) {
		t.Fatal("rename changed group identity or timestamps", updated, err)
	}
	f.clock.Advance(time.Minute)
	unchanged, err := svc.Update(t.Context(), adminPrincipal, group.ID, "Team")
	if err != nil || !unchanged.UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatal("no-op rename changed updated_at", err)
	}
	_, err = svc.Update(t.Context(), adminPrincipal, group.ID, "operators")
	requireCode(t, err, "ALREADY_EXISTS")
	session := createSession(t, f.db, f.sessions, testkit.UserView)
	if _, err := svc.Update(t.Context(), adminPrincipal, viewGroup, "renamed readers"); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.sessions.Resolve(t.Context(), session.Token)
	if err != nil || resolved.Token != "" {
		t.Fatal("rename invalidated a session", err)
	}
	level, err := access.NewPolicy(f.db).Effective(t.Context(), testkit.UserView, testkit.DBPG)
	if err != nil || level != contracts.AccessView {
		t.Fatal("rename changed target access", err)
	}
}

func TestGroupValidationAndMembers(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	for _, name := range []string{"", " \t\n", strings.Repeat("x", 101), "bad\x00name", "bad\xffname"} {
		_, err := svc.Create(t.Context(), adminPrincipal, name)
		requireCode(t, err, "INVALID_REQUEST")
		_, err = svc.Update(t.Context(), adminPrincipal, viewGroup, name)
		requireCode(t, err, "INVALID_REQUEST")
	}
	empty, err := svc.Create(t.Context(), adminPrincipal, strings.Repeat("界", 100))
	if err != nil {
		t.Fatal("group length should count characters instead of bytes", err)
	}
	members, more, err := svc.Members(t.Context(), adminPrincipal, empty.ID, 50, 0)
	if err != nil || more || members == nil || len(members) != 0 {
		t.Fatal("empty group should have an empty member list", err)
	}
	for _, page := range [][2]int{{0, 0}, {201, 0}, {50, -1}, {50, 100001}} {
		_, _, err := svc.List(t.Context(), adminPrincipal, page[0], page[1])
		requireCode(t, err, "INVALID_REQUEST")
		_, _, err = svc.Members(t.Context(), adminPrincipal, viewGroup, page[0], page[1])
		requireCode(t, err, "INVALID_REQUEST")
	}
	for _, id := range []string{"bad-id", missingUser} {
		code := "NOT_FOUND"
		if id == "bad-id" {
			code = "INVALID_REQUEST"
		}
		_, err := svc.Update(t.Context(), adminPrincipal, id, "new name")
		requireCode(t, err, code)
		requireCode(t, svc.Delete(t.Context(), adminPrincipal, id), code)
		_, _, err = svc.Members(t.Context(), adminPrincipal, id, 50, 0)
		requireCode(t, err, code)
		requireCode(t, svc.AddMember(t.Context(), adminPrincipal, id, testkit.UserView), code)
		requireCode(t, svc.RemoveMember(t.Context(), adminPrincipal, viewGroup, id), code)
	}
	members, more, err = svc.Members(t.Context(), adminPrincipal, viewGroup, 1, 0)
	if err != nil || !more || len(members) != 1 || members[0].ID != testkit.UserView {
		t.Fatal("members must be ordered by user ID", members, more, err)
	}
	members, more, err = svc.Members(t.Context(), adminPrincipal, viewGroup, 1, 1)
	if err != nil || more || len(members) != 1 || members[0].ID != testkit.UserEdit {
		t.Fatal("incorrect final member page", members, more, err)
	}
	if _, err := f.db.Exec(`UPDATE users SET active=false WHERE id=$1`, testkit.UserView); err != nil {
		t.Fatal(err)
	}
	members, _, err = svc.Members(t.Context(), adminPrincipal, viewGroup, 50, 0)
	if err != nil || len(members) != 2 || members[0].Active {
		t.Fatal("inactive users should remain listed as members", err)
	}
}

func TestGroupMembershipRotationAndIdempotence(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	session := createSession(t, f.db, f.sessions, testkit.UserNone)
	second := createSession(t, f.db, f.sessions, testkit.UserNone)
	other := createSession(t, f.db, f.sessions, testkit.UserView)
	f.clock.Advance(15 * time.Minute)
	current, err := f.sessions.Resolve(t.Context(), session.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(t.Context(), adminPrincipal, editGroup, testkit.UserNone); err != nil {
		t.Fatal(err)
	}
	_, err = f.sessions.Resolve(t.Context(), session.Token)
	requireCode(t, err, "UNAUTHENTICATED")
	elevated, err := f.sessions.Resolve(t.Context(), current.Token)
	if err != nil || elevated.Token == "" || !elevated.AbsoluteExpiresAt.Equal(session.AbsoluteExpiresAt) {
		t.Fatal("membership did not require rotation without extending the deadline", err)
	}
	otherSession, err := f.sessions.Resolve(t.Context(), second.Token)
	if err != nil || otherSession.Token == "" {
		t.Fatal("all user sessions must rotate after membership changes", err)
	}
	if err := access.NewPolicy(f.db).Require(t.Context(), elevated.Principal, contracts.EditDatabase, testkit.DBPG); err != nil {
		t.Fatal("new group grant did not take effect", err)
	}
	var before time.Time
	if err := f.db.QueryRow(`SELECT created_at FROM group_memberships WHERE group_id=$1 AND user_id=$2`, editGroup, testkit.UserNone).Scan(&before); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	if err := svc.AddMember(t.Context(), adminPrincipal, editGroup, testkit.UserNone); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.sessions.Resolve(t.Context(), elevated.Token)
	if err != nil || resolved.Token != "" {
		t.Fatal("duplicate membership forced another rotation", err)
	}
	var after time.Time
	if err := f.db.QueryRow(`SELECT created_at FROM group_memberships WHERE group_id=$1 AND user_id=$2`, editGroup, testkit.UserNone).Scan(&after); err != nil || !after.Equal(before) {
		t.Fatal("duplicate membership changed its creation time", err)
	}
	if err := svc.RemoveMember(t.Context(), adminPrincipal, editGroup, testkit.UserNone); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{current.Token, elevated.Token, otherSession.Token} {
		_, err := f.sessions.Resolve(t.Context(), token)
		requireCode(t, err, "UNAUTHENTICATED")
	}
	if _, err := f.sessions.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("unrelated user's session was revoked", err)
	}
	fresh := createSession(t, f.db, f.sessions, testkit.UserNone)
	if err := svc.RemoveMember(t.Context(), adminPrincipal, editGroup, testkit.UserNone); err != nil {
		t.Fatal("repeated removal should be harmless", err)
	}
	if _, err := f.sessions.Resolve(t.Context(), fresh.Token); err != nil {
		t.Fatal("removing a missing membership revoked a fresh session", err)
	}
	level, err := access.NewPolicy(f.db).Effective(t.Context(), testkit.UserNone, testkit.DBPG)
	if err != nil || level != contracts.AccessNone {
		t.Fatal("removed membership retained access", err)
	}
}

func TestDeleteGroupRevokesMembersAndCascades(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	reader := createSession(t, f.db, f.sessions, testkit.UserView)
	writer := createSession(t, f.db, f.sessions, testkit.UserEdit)
	other := createSession(t, f.db, f.sessions, testkit.UserNone)
	prelogin := createSession(t, f.db, f.sessions, "")
	f.clock.Advance(15 * time.Minute)
	rotated, err := f.sessions.Resolve(t.Context(), reader.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(t.Context(), adminPrincipal, viewGroup); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{reader.Token, rotated.Token, writer.Token} {
		_, err := f.sessions.Resolve(t.Context(), token)
		requireCode(t, err, "UNAUTHENTICATED")
	}
	if _, err := f.sessions.Resolve(t.Context(), other.Token); err != nil {
		t.Fatal("unrelated user was revoked", err)
	}
	var remaining, users int
	if err := f.db.QueryRow(`SELECT
		(SELECT count(*) FROM groups WHERE id=$1) +
		(SELECT count(*) FROM group_memberships WHERE group_id=$1) +
		(SELECT count(*) FROM group_database_access WHERE group_id=$1),
		(SELECT count(*) FROM users)`, viewGroup).Scan(&remaining, &users); err != nil || remaining != 0 || users != 4 {
		t.Fatal("group deletion did not clean up only its own memberships and grants", err)
	}
	var revoked bool
	if err := f.db.QueryRow(`SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1`, prelogin.ID).Scan(&revoked); err != nil || revoked {
		t.Fatal("group deletion revoked anonymous sessions", err)
	}
	policy := access.NewPolicy(f.db)
	for id, want := range map[string]contracts.AccessLevel{testkit.UserView: contracts.AccessNone, testkit.UserEdit: contracts.AccessEdit} {
		level, err := policy.Effective(t.Context(), id, testkit.DBPG)
		if err != nil || level != want {
			t.Fatal("other group grants were not preserved", id, level, err)
		}
	}
}

func TestGroupSessionFailuresRollBackChanges(t *testing.T) {
	f := newUserFixture(t)
	broken := access.NewGroupService(f.db, failingUserSessions{f.sessions}, access.NewPolicy(f.db), f.clock)
	outsider := createSession(t, f.db, f.sessions, testkit.UserNone)
	reader := createSession(t, f.db, f.sessions, testkit.UserView)
	writer := createSession(t, f.db, f.sessions, testkit.UserEdit)
	requireCode(t, broken.AddMember(t.Context(), adminPrincipal, editGroup, testkit.UserNone), "INTERNAL_ERROR")
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM group_memberships WHERE user_id=$1`, testkit.UserNone).Scan(&count); err != nil || count != 0 {
		t.Fatal("membership addition survived rollback", err)
	}
	resolved, err := f.sessions.Resolve(t.Context(), outsider.Token)
	if err != nil || resolved.Token != "" {
		t.Fatal("rotation survived membership rollback", err)
	}
	requireCode(t, broken.RemoveMember(t.Context(), adminPrincipal, viewGroup, testkit.UserView), "INTERNAL_ERROR")
	requireCode(t, broken.Delete(t.Context(), adminPrincipal, viewGroup), "INTERNAL_ERROR")
	if err := f.db.QueryRow(`SELECT count(*) FROM group_memberships WHERE group_id=$1`, viewGroup).Scan(&count); err != nil || count != 2 {
		t.Fatal("membership deletion survived rollback", err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM group_database_access WHERE group_id=$1`, viewGroup).Scan(&count); err != nil || count != 1 {
		t.Fatal("grant deletion survived rollback", err)
	}
	for _, token := range []string{reader.Token, writer.Token} {
		if _, err := f.sessions.Resolve(t.Context(), token); err != nil {
			t.Fatal("revocation survived group rollback", err)
		}
	}
	if _, err := groupService(f).Update(t.Context(), adminPrincipal, viewGroup, "still exists"); err != nil {
		t.Fatal("group deletion survived rollback", err)
	}
}

func TestGroupConcurrentAddAndDelete(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	group, err := svc.Create(t.Context(), adminPrincipal, "concurrent")
	if err != nil {
		t.Fatal(err)
	}
	session := createSession(t, f.db, f.sessions, testkit.UserNone)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	added, deleted := make(chan error, 1), make(chan error, 1)
	go func() { <-start; added <- svc.AddMember(ctx, adminPrincipal, group.ID, testkit.UserNone) }()
	go func() { <-start; deleted <- svc.Delete(ctx, adminPrincipal, group.ID) }()
	close(start)
	addErr, deleteErr := <-added, <-deleted
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	_, _, err = svc.Members(t.Context(), adminPrincipal, group.ID, 50, 0)
	requireCode(t, err, "NOT_FOUND")
	_, sessionErr := f.sessions.Resolve(t.Context(), session.Token)
	if addErr == nil {
		requireCode(t, sessionErr, "UNAUTHENTICATED")
	} else {
		requireCode(t, addErr, "NOT_FOUND")
		if sessionErr != nil {
			t.Fatal("failed addition changed an unrelated session", sessionErr)
		}
	}
}

func TestGroupStorageFailures(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := svc.List(ctx, adminPrincipal, 50, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause was lost", err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.Members(t.Context(), adminPrincipal, viewGroup, 50, 0)
	requireCode(t, err, "INTERNAL_ERROR")
	if strings.Contains(err.Error(), "database is closed") {
		t.Fatal("storage failure leaked details")
	}
}
