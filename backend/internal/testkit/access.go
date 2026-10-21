package testkit

import (
	"context"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

const (
	UserAdmin       = "10000000-0000-4000-8000-000000000001"
	UserView        = "10000000-0000-4000-8000-000000000002"
	UserEdit        = "10000000-0000-4000-8000-000000000003"
	UserNone        = "10000000-0000-4000-8000-000000000004"
	DBPG            = "30000000-0000-4000-8000-000000000001"
	DBSQLite        = "30000000-0000-4000-8000-000000000002"
	DBPGDesired     = "30000000-0000-4000-8000-000000000003"
	DBSQLiteDesired = "30000000-0000-4000-8000-000000000004"
)

type AccessChecker struct {
	grants map[string]map[string]contracts.AccessLevel
	admins map[string]bool
}

var _ contracts.AccessChecker = (*AccessChecker)(nil)

func NewFixtureAccessChecker() *AccessChecker {
	return &AccessChecker{
		admins: map[string]bool{UserAdmin: true},
		grants: map[string]map[string]contracts.AccessLevel{
			UserView: {DBPG: contracts.AccessView, DBSQLite: contracts.AccessView, DBPGDesired: contracts.AccessView, DBSQLiteDesired: contracts.AccessView},
			UserEdit: {DBPG: contracts.AccessEdit, DBSQLite: contracts.AccessEdit, DBPGDesired: contracts.AccessView, DBSQLiteDesired: contracts.AccessView},
		},
	}
}

func (a *AccessChecker) Effective(ctx context.Context, userID, databaseID string) (contracts.AccessLevel, error) {
	if err := ctx.Err(); err != nil {
		return contracts.AccessNone, err
	}
	if level := a.grants[userID][databaseID]; level != "" {
		return level, nil
	}
	return contracts.AccessNone, nil
}

func (a *AccessChecker) Require(ctx context.Context, p contracts.Principal, action contracts.Action, databaseID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.UserID == "" {
		return &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Authentication required"}
	}
	if action == contracts.ManagePlatform && databaseID == "" && p.IsPlatformAdmin && a.admins[p.UserID] {
		return nil
	}
	if action != contracts.ViewDatabase && action != contracts.EditDatabase {
		return &contracts.DomainError{Code: "FORBIDDEN", Message: "Access denied"}
	}
	level, err := a.Effective(ctx, p.UserID, databaseID)
	if err != nil {
		return err
	}
	if level == contracts.AccessNone {
		return &contracts.DomainError{Code: "NOT_FOUND", Message: "Database not found"}
	}
	if action == contracts.EditDatabase && level != contracts.AccessEdit {
		return &contracts.DomainError{Code: "FORBIDDEN", Message: "Access denied"}
	}
	return nil
}
