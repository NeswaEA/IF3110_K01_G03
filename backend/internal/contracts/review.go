package contracts

import (
	"context"
	"database/sql"
	"time"
)

// ReviewSubject is a change request as the review module sees it; the
// migrations module owns the row.
type ReviewSubject struct {
	RequestID, DatabaseID, MigrationID string
	Action                             string
	Revision                           int64
	ContentHash                        []byte
	RequestedBy                        string
	Status                             string // stored RequestStatus
}

type RequiredApproval struct {
	GroupID, GroupName   string
	LeaderID, LeaderName *string
	Decision             string // pending|approved|rejected
	Comment              *string
	DecidedAt            *time.Time
}

type ReviewState struct {
	Status   string // pending_review|approved|rejected
	Required []RequiredApproval
}

type ReviewEvaluator interface {
	// Evaluate computes the required groups and active decisions; it never writes.
	Evaluate(ctx context.Context, q Querier, s ReviewSubject) (ReviewState, error)
	// DismissApprovals sets dismissed_at on every active approval of the request; it does not commit tx.
	DismissApprovals(ctx context.Context, tx *sql.Tx, requestID string) error
}

type ReviewSubjects interface {
	// LoadSubject returns NOT_FOUND when the request does not exist.
	LoadSubject(ctx context.Context, q Querier, requestID string) (ReviewSubject, error)
	// ApplyReviewState moves an open request to pending_review, approved or rejected; otherwise REQUEST_NOT_OPEN.
	ApplyReviewState(ctx context.Context, tx *sql.Tx, requestID, status string) error
}
