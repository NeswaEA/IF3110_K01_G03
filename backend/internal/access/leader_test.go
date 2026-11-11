package access_test

import (
	"encoding/json"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestGroupLeaderAssignment(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	group, err := svc.Get(t.Context(), adminPrincipal, viewGroup)
	if err != nil || group.Leader != nil {
		t.Fatal("group without a leader must report none", group, err)
	}
	group, err = svc.SetLeader(t.Context(), adminPrincipal, viewGroup, testkit.UserView)
	if err != nil || group.Leader == nil || group.Leader.ID != testkit.UserView || group.Leader.Username != "viewer" {
		t.Fatal("leader was not assigned", group, err)
	}
	group, err = svc.SetLeader(t.Context(), adminPrincipal, viewGroup, testkit.UserEdit)
	if err != nil || group.Leader.ID != testkit.UserEdit {
		t.Fatal("leader was not replaced", group, err)
	}
	// The former leader can now leave the group.
	if err := svc.RemoveMember(t.Context(), adminPrincipal, viewGroup, testkit.UserView); err != nil {
		t.Fatal("former leader could not be removed", err)
	}
}

func TestGroupLeaderMustBeActiveMember(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	if _, err := f.users.Update(t.Context(), adminPrincipal, testkit.UserView, access.UpdateUserInput{Active: flag(false)}); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{testkit.UserNone, testkit.UserView, missingUser, "invalid"} {
		_, err := svc.SetLeader(t.Context(), adminPrincipal, viewGroup, userID)
		requireCode(t, err, "INVALID_REQUEST")
	}
	_, err := svc.SetLeader(t.Context(), adminPrincipal, missingUser, testkit.UserEdit)
	requireCode(t, err, "NOT_FOUND")
	_, err = svc.SetLeader(t.Context(), contracts.Principal{UserID: testkit.UserEdit}, viewGroup, testkit.UserEdit)
	requireCode(t, err, "FORBIDDEN")
	group, err := svc.Get(t.Context(), adminPrincipal, viewGroup)
	if err != nil || group.Leader != nil {
		t.Fatal("rejected assignment stored a leader", err)
	}
}

func TestLeaderInvariants(t *testing.T) {
	f := newUserFixture(t)
	svc := groupService(f)
	// readers already holds a grant without a leader, so only new grants are checked.
	group, err := svc.Create(t.Context(), adminPrincipal, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(t.Context(), adminPrincipal, group.ID, testkit.UserNone); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SetGrant(t.Context(), adminPrincipal, group.ID, testkit.DBPG, contracts.AccessView)
	requireCode(t, err, "LEADER_REQUIRED")
	if grants, _, err := svc.Grants(t.Context(), adminPrincipal, group.ID, 10, 0); err != nil || len(grants) != 0 {
		t.Fatal("rejected grant was stored", grants, err)
	}
	if _, err := svc.SetLeader(t.Context(), adminPrincipal, group.ID, testkit.UserNone); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetGrant(t.Context(), adminPrincipal, group.ID, testkit.DBPG, contracts.AccessView); err != nil {
		t.Fatal("grant with a leader failed", err)
	}
	requireCode(t, svc.RemoveMember(t.Context(), adminPrincipal, group.ID, testkit.UserNone), "LEADER_REQUIRED")
	if members, _, err := svc.Members(t.Context(), adminPrincipal, group.ID, 10, 0); err != nil || len(members) != 1 {
		t.Fatal("rejected removal changed membership", members, err)
	}
	_, err = f.users.Update(t.Context(), adminPrincipal, testkit.UserNone, access.UpdateUserInput{Active: flag(false)})
	requireCode(t, err, "LEADER_REQUIRED")
	var active bool
	if err := f.db.QueryRow(`SELECT active FROM users WHERE id=$1`, testkit.UserNone).Scan(&active); err != nil || !active {
		t.Fatal("rejected deactivation changed the user", err)
	}
	if err := svc.RemoveGrant(t.Context(), adminPrincipal, group.ID, testkit.DBPG); err != nil {
		t.Fatal(err)
	}
	if _, err := f.users.Update(t.Context(), adminPrincipal, testkit.UserNone, access.UpdateUserInput{Active: flag(false)}); err != nil {
		t.Fatal("leader of a group without grants must be deactivatable", err)
	}
	if err := svc.Delete(t.Context(), adminPrincipal, group.ID); err != nil {
		t.Fatal("group with a leader could not be deleted", err)
	}
}

func TestGroupHTTPSetLeader(t *testing.T) {
	f := newUserHTTPFixture(t)
	path := groupsPath + "/" + viewGroup + "/leader"
	body := `{"user_id":"` + testkit.UserEdit + `"}`
	assertUserHTTPError(t, f.request("PUT", path, body, nil, false), 401, "UNAUTHENTICATED")
	assertUserHTTPError(t, f.request("PUT", path, body, &f.viewer, true), 403, "FORBIDDEN")
	assertUserHTTPError(t, f.request("PUT", path, body, &f.admin, false), 403, "CSRF_REJECTED")
	assertUserHTTPError(t, f.request("PUT", path, `{"user_id":"`+testkit.UserNone+`"}`, &f.admin, true), 400, "INVALID_REQUEST")
	w := f.request("PUT", path, body, &f.admin, true)
	var result struct {
		Data access.Group `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Data.Leader == nil || result.Data.Leader.Username != "editor" {
		t.Fatal("leader assignment failed", err, w.Body.String())
	}
}
