package contracts

import (
	"context"
	"database/sql"
)

type Action string
type AccessLevel string

const (
	ManagePlatform Action      = "platform.manage"
	ViewDatabase   Action      = "database.view"
	EditDatabase   Action      = "database.edit"
	AccessNone     AccessLevel = "none"
	AccessView     AccessLevel = "view"
	AccessEdit     AccessLevel = "edit"
)

type AccessChecker interface {
	Require(ctx context.Context, p Principal, action Action, databaseID string) error
	Effective(ctx context.Context, userID, databaseID string) (AccessLevel, error)
}

type SessionInvalidator interface {
	RevokeUsers(ctx context.Context, tx *sql.Tx, userIDs []string) error
	RequireRotation(ctx context.Context, tx *sql.Tx, userIDs []string) error
}

type DatabaseAccessLifecycle interface {
	Disable(ctx context.Context, tx *sql.Tx, databaseID string) error
}
