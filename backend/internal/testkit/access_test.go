package testkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestFixtureAccess(t *testing.T) {
	policy := testkit.NewFixtureAccessChecker()
	tests := []struct {
		name, user, database string
		admin                bool
		action               contracts.Action
		code                 string
	}{
		{"anonymous", "", testkit.DBPG, false, contracts.ViewDatabase, "UNAUTHENTICATED"},
		{"admin platform", testkit.UserAdmin, "", true, contracts.ManagePlatform, ""},
		{"admin without grant", testkit.UserAdmin, testkit.DBPG, true, contracts.ViewDatabase, "NOT_FOUND"},
		{"nonadmin platform", testkit.UserView, "", false, contracts.ManagePlatform, "FORBIDDEN"},
		{"forged admin", testkit.UserView, "", true, contracts.ManagePlatform, "FORBIDDEN"},
		{"view", testkit.UserView, testkit.DBPG, false, contracts.ViewDatabase, ""},
		{"view cannot edit", testkit.UserView, testkit.DBPG, false, contracts.EditDatabase, "FORBIDDEN"},
		{"edit", testkit.UserEdit, testkit.DBSQLite, false, contracts.EditDatabase, ""},
		{"edit can view", testkit.UserEdit, testkit.DBPG, false, contracts.ViewDatabase, ""},
		{"desired view", testkit.UserEdit, testkit.DBPGDesired, false, contracts.ViewDatabase, ""},
		{"desired cannot edit", testkit.UserEdit, testkit.DBSQLiteDesired, false, contracts.EditDatabase, "FORBIDDEN"},
		{"no grant", testkit.UserNone, testkit.DBPG, false, contracts.ViewDatabase, "NOT_FOUND"},
		{"unknown user", "unknown", testkit.DBPG, false, contracts.ViewDatabase, "NOT_FOUND"},
		{"unknown database", testkit.UserEdit, "unknown", false, contracts.ViewDatabase, "NOT_FOUND"},
		{"unknown action", testkit.UserEdit, testkit.DBPG, false, "unknown", "FORBIDDEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := contracts.Principal{UserID: tt.user, IsPlatformAdmin: tt.admin}
			err := policy.Require(context.Background(), p, tt.action, tt.database)
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var domain *contracts.DomainError
			if !errors.As(err, &domain) || domain.Code != tt.code {
				t.Fatalf("expected %s, got %v", tt.code, err)
			}
		})
	}
}

func TestZeroAccessCheckerDenies(t *testing.T) {
	var policy testkit.AccessChecker
	ctx := context.Background()
	level, err := policy.Effective(ctx, testkit.UserEdit, testkit.DBPG)
	if err != nil || level != contracts.AccessNone {
		t.Fatalf("got %s, %v", level, err)
	}
	p := contracts.Principal{UserID: testkit.UserAdmin, IsPlatformAdmin: true}
	if policy.Require(ctx, p, contracts.ManagePlatform, "") == nil {
		t.Fatal("zero policy allowed admin")
	}
	if policy.Require(ctx, p, contracts.ViewDatabase, testkit.DBPG) == nil {
		t.Fatal("zero policy allowed database")
	}
}

func TestAccessCheckerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy := testkit.NewFixtureAccessChecker()
	if err := policy.Require(ctx, contracts.Principal{UserID: testkit.UserEdit}, contracts.EditDatabase, testkit.DBPG); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := policy.Effective(ctx, testkit.UserEdit, testkit.DBPG); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
