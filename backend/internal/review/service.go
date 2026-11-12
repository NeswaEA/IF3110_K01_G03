package review

import (
	"context"
	"database/sql"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// Service decides the approval status of change requests; the migrations module owns the requests.
type Service struct {
	db       *sql.DB
	subjects contracts.ReviewSubjects
	audit    contracts.AuditRecorder
	events   contracts.EventPublisher
	clock    contracts.Clock
}

var _ contracts.ReviewEvaluator = (*Service)(nil)

func NewService(db *sql.DB, subjects contracts.ReviewSubjects, audit contracts.AuditRecorder, events contracts.EventPublisher, clock contracts.Clock) *Service {
	return &Service{db: db, subjects: subjects, audit: audit, events: events, clock: clock}
}

// Evaluate recomputes the required groups from the live grants. A decision counts only when it was made
// by the group's current active leader for the request's current revision.
func (s *Service) Evaluate(ctx context.Context, q contracts.Querier, subject contracts.ReviewSubject) (contracts.ReviewState, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.name, u.id, u.username, a.decision, a.comment, a.created_at
		FROM group_database_access ga
		JOIN groups g ON g.id = ga.group_id
		LEFT JOIN users u ON u.id = g.leader_user_id AND u.active
		LEFT JOIN change_request_approvals a ON a.request_id = $2 AND a.group_id = g.id AND a.dismissed_at IS NULL
			AND a.approver_id = u.id AND a.migration_revision = $3
		WHERE ga.database_id = $1
		ORDER BY g.name, g.id`, subject.DatabaseID, subject.RequestID, subject.Revision)
	if err != nil {
		return contracts.ReviewState{}, failure(err)
	}
	defer rows.Close()
	state := contracts.ReviewState{Required: []contracts.RequiredApproval{}}
	approved := 0
	rejected := false
	for rows.Next() {
		var r contracts.RequiredApproval
		var decision sql.NullString
		if err := rows.Scan(&r.GroupID, &r.GroupName, &r.LeaderID, &r.LeaderName, &decision, &r.Comment, &r.DecidedAt); err != nil {
			return contracts.ReviewState{}, failure(err)
		}
		r.Decision = "pending"
		if decision.Valid {
			r.Decision = decision.String
		}
		if r.DecidedAt != nil {
			at := r.DecidedAt.UTC()
			r.DecidedAt = &at
		}
		switch r.Decision {
		case "approved":
			approved++
		case "rejected":
			rejected = true
		}
		state.Required = append(state.Required, r)
	}
	if err := rows.Err(); err != nil {
		return contracts.ReviewState{}, failure(err)
	}
	switch {
	case rejected:
		state.Status = "rejected"
	case approved > 0 && approved == len(state.Required):
		state.Status = "approved"
	default:
		state.Status = "pending_review"
	}
	return state, nil
}

// DismissApprovals retires every active decision of the request; the caller commits tx.
func (s *Service) DismissApprovals(ctx context.Context, tx *sql.Tx, requestID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE change_request_approvals SET dismissed_at = $2
		WHERE request_id = $1 AND dismissed_at IS NULL`, requestID, s.clock.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		return failure(err)
	}
	return nil
}

func failure(err error) error {
	return &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Pemeriksaan approval gagal.", Cause: err}
}
