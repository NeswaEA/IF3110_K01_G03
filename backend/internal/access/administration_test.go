package access_test

import (
	"context"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestAdministrationServicesRequireCurrentAdmin(t *testing.T) {
	f := newUserFixture(t)
	groups := groupService(f)
	operations := map[string]func(context.Context, contracts.Principal) error{
		"users.list": func(ctx context.Context, p contracts.Principal) error {
			_, _, err := f.users.List(ctx, p, 50, 0)
			return err
		},
		"users.create": func(ctx context.Context, p contracts.Principal) error {
			_, err := f.users.Create(ctx, p, access.CreateUserInput{Username: "blocked-user", Password: userPassword})
			return err
		},
		"users.update": func(ctx context.Context, p contracts.Principal) error {
			_, err := f.users.Update(ctx, p, testkit.UserView, access.UpdateUserInput{Active: flag(false)})
			return err
		},
		"users.reset_password": func(ctx context.Context, p contracts.Principal) error {
			return f.users.ResetPassword(ctx, p, testkit.UserView, userPassword)
		},
		"groups.list": func(ctx context.Context, p contracts.Principal) error {
			_, _, err := groups.List(ctx, p, 50, 0)
			return err
		},
		"groups.create": func(ctx context.Context, p contracts.Principal) error {
			_, err := groups.Create(ctx, p, "blocked group")
			return err
		},
		"groups.update": func(ctx context.Context, p contracts.Principal) error {
			_, err := groups.Update(ctx, p, viewGroup, "blocked rename")
			return err
		},
		"groups.delete": func(ctx context.Context, p contracts.Principal) error {
			return groups.Delete(ctx, p, viewGroup)
		},
		"members.list": func(ctx context.Context, p contracts.Principal) error {
			_, _, err := groups.Members(ctx, p, viewGroup, 50, 0)
			return err
		},
		"members.add": func(ctx context.Context, p contracts.Principal) error {
			return groups.AddMember(ctx, p, viewGroup, testkit.UserNone)
		},
		"members.remove": func(ctx context.Context, p contracts.Principal) error {
			return groups.RemoveMember(ctx, p, viewGroup, testkit.UserView)
		},
		"grants.list": func(ctx context.Context, p contracts.Principal) error {
			_, _, err := groups.Grants(ctx, p, viewGroup, 50, 0)
			return err
		},
		"grants.set": func(ctx context.Context, p contracts.Principal) error {
			_, err := groups.SetGrant(ctx, p, viewGroup, testkit.DBPG, contracts.AccessEdit)
			return err
		},
		"grants.remove": func(ctx context.Context, p contracts.Principal) error {
			return groups.RemoveGrant(ctx, p, viewGroup, testkit.DBPG)
		},
	}
	if _, err := f.db.Exec(`UPDATE users SET active=false WHERE id=$1`, testkit.UserNone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE users SET is_platform_admin=false WHERE id=$1`, testkit.UserAdmin); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []struct {
		name      string
		principal contracts.Principal
		code      string
	}{
		{"anonymous", contracts.Principal{}, "UNAUTHENTICATED"},
		{"viewer_claims_admin", contracts.Principal{UserID: testkit.UserView, IsPlatformAdmin: true}, "FORBIDDEN"},
		{"editor", contracts.Principal{UserID: testkit.UserEdit}, "FORBIDDEN"},
		{"inactive", contracts.Principal{UserID: testkit.UserNone, IsPlatformAdmin: true}, "UNAUTHENTICATED"},
		{"missing", contracts.Principal{UserID: missingUser, IsPlatformAdmin: true}, "UNAUTHENTICATED"},
		{"demoted_admin", adminPrincipal, "FORBIDDEN"},
	} {
		t.Run(actor.name, func(t *testing.T) {
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) {
					requireCode(t, operation(t.Context(), actor.principal), actor.code)
				})
			}
		})
	}
}
