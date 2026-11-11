package review

import (
	"context"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/google/uuid"
)

// Approve records the actor's approval for every required group they lead that is still pending.
func (s *Service) Approve(ctx context.Context, actor contracts.Principal, databaseID, requestID string, comment *string) error {
	if comment != nil {
		trimmed := strings.TrimSpace(*comment)
		if trimmed == "" {
			comment = nil
		} else if err := validComment(trimmed); err != nil {
			return err
		} else {
			comment = &trimmed
		}
	}
	return s.decide(ctx, actor, databaseID, requestID, "approved", comment)
}

// Reject records the actor's rejection; a reason is mandatory.
func (s *Service) Reject(ctx context.Context, actor contracts.Principal, databaseID, requestID, comment string) error {
	comment = strings.TrimSpace(comment)
	if err := validComment(comment); err != nil {
		return err
	}
	return s.decide(ctx, actor, databaseID, requestID, "rejected", &comment)
}

func (s *Service) decide(ctx context.Context, actor contracts.Principal, databaseID, requestID, decision string, comment *string) error {
	if !contracts.ValidID(databaseID) || !contracts.ValidID(requestID) {
		return notFound()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return failure(err)
	}
	defer tx.Rollback()
	subject, err := s.subjects.LoadSubject(ctx, tx, requestID)
	if err != nil {
		return err
	}
	if subject.DatabaseID != databaseID {
		return notFound()
	}
	if subject.Status != "pending_review" && subject.Status != "approved" {
		return &contracts.DomainError{Code: "REQUEST_NOT_OPEN", Message: "Change request sudah tidak terbuka."}
	}
	state, err := s.Evaluate(ctx, tx, subject)
	if err != nil {
		return err
	}
	var groups []string
	for _, r := range state.Required {
		if r.LeaderID != nil && *r.LeaderID == actor.UserID && r.Decision == "pending" {
			groups = append(groups, r.GroupID)
		}
	}
	if len(groups) == 0 {
		return &contracts.DomainError{Code: "NOT_LEADER", Message: "Anda bukan leader grup yang approval-nya masih dibutuhkan."}
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	for _, group := range groups {
		// A pending group may still hold a decision from a former leader or an older revision; it no longer counts.
		if _, err := tx.ExecContext(ctx, `UPDATE change_request_approvals SET dismissed_at = $3
			WHERE request_id = $1 AND group_id = $2 AND dismissed_at IS NULL`, requestID, group, now); err != nil {
			return failure(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO change_request_approvals
			(id, request_id, group_id, approver_id, decision, comment, migration_revision, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			uuid.Must(uuid.NewV7()).String(), requestID, group, actor.UserID, decision, comment, subject.Revision, now); err != nil {
			return failure(err)
		}
	}
	state, err = s.Evaluate(ctx, tx, subject)
	if err != nil {
		return err
	}
	if err := s.subjects.ApplyReviewState(ctx, tx, requestID, state.Status); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return failure(err)
	}
	event := "review." + decision
	slog.InfoContext(ctx, "change request "+decision, "event", event, "database_id", databaseID,
		"change_request_id", requestID, "migration_id", subject.MigrationID, "migration_revision", subject.Revision, "status", state.Status)
	slices.Sort(groups)
	s.audit.Record(ctx, contracts.AuditEvent{
		Action: "request." + decision, ActorID: &actor.UserID, ActorName: &actor.Username, DatabaseID: &databaseID,
		ResourceType: "change_request", ResourceID: requestID,
		Details: map[string]any{"migration_id": subject.MigrationID, "action": subject.Action, "revision": subject.Revision,
			"content_hash": hex.EncodeToString(subject.ContentHash), "group_ids": groups, "status": state.Status},
	})
	return nil
}

func validComment(comment string) error {
	if n := utf8.RuneCountInString(comment); !utf8.ValidString(comment) || n < 1 || n > 2000 {
		return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Komentar tidak valid.",
			Fields: map[string]string{"comment": "Isi komentar 1-2000 karakter."}}
	}
	return nil
}

func notFound() error {
	return &contracts.DomainError{Code: "NOT_FOUND", Message: "Change request tidak ditemukan."}
}
