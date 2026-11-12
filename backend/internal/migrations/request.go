package migrations

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/google/uuid"
)

type NamedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RequiredApproval struct {
	Group     NamedRef   `json:"group"`
	Leader    *Actor     `json:"leader"`
	Decision  string     `json:"decision"`
	Comment   *string    `json:"comment"`
	DecidedAt *time.Time `json:"decided_at"`
}

type ChangeRequest struct {
	ID                string             `json:"id"`
	DatabaseID        string             `json:"database_id"`
	MigrationID       string             `json:"migration_id"`
	MigrationTitle    string             `json:"migration_title"`
	Action            string             `json:"action"`
	MigrationRevision int64              `json:"migration_revision"`
	ContentHash       string             `json:"content_hash"`
	HeadAtRequest     int64              `json:"head_at_request"`
	TargetExecutionID *string            `json:"target_execution_id"`
	BatchSize         *int               `json:"batch_size"`
	Note              string             `json:"note"`
	Status            string             `json:"status"`
	RequestedBy       Actor              `json:"requested_by"`
	Approvals         []RequiredApproval `json:"approvals"`
	ExecutionID       *string            `json:"execution_id"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

type requestInput struct {
	Action            string  `json:"action"`
	TargetExecutionID *string `json:"target_execution_id"`
	BatchSize         *int    `json:"batch_size"`
	Note              string  `json:"note"`
}

type requestRow struct {
	ChangeRequest
	hash []byte
}

const requestSelect = `SELECT cr.id, cr.database_id, cr.migration_id, m.title, cr.action, cr.migration_revision,
	cr.content_hash, cr.head_at_request, cr.target_execution_id, cr.batch_size, cr.note, cr.status,
	cr.requested_by, u.username, e.id, cr.created_at, cr.updated_at
	FROM change_requests cr
	JOIN migrations m ON m.id=cr.migration_id
	JOIN users u ON u.id=cr.requested_by
	LEFT JOIN migration_executions e ON e.change_request_id=cr.id`

func scanRequest(row scanner) (requestRow, error) {
	var r requestRow
	err := row.Scan(&r.ID, &r.DatabaseID, &r.MigrationID, &r.MigrationTitle, &r.Action, &r.MigrationRevision,
		&r.hash, &r.HeadAtRequest, &r.TargetExecutionID, &r.BatchSize, &r.Note, &r.Status, &r.RequestedBy.ID,
		&r.RequestedBy.Username, &r.ExecutionID, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Service) renderRequest(ctx context.Context, q contracts.Querier, r requestRow) (ChangeRequest, error) {
	state, err := s.review.Evaluate(ctx, q, contracts.ReviewSubject{
		RequestID: r.ID, DatabaseID: r.DatabaseID, MigrationID: r.MigrationID, Action: r.Action,
		Revision: r.MigrationRevision, ContentHash: r.hash, RequestedBy: r.RequestedBy.ID, Status: r.Status,
	})
	if err != nil {
		return ChangeRequest{}, err
	}
	r.ContentHash = hashString(r.hash)
	r.Approvals = make([]RequiredApproval, 0, len(state.Required))
	for _, group := range state.Required {
		approval := RequiredApproval{Group: NamedRef{ID: group.GroupID, Name: group.GroupName},
			Decision: group.Decision, Comment: group.Comment, DecidedAt: group.DecidedAt}
		if group.LeaderID != nil && group.LeaderName != nil {
			approval.Leader = &Actor{ID: *group.LeaderID, Username: *group.LeaderName}
		}
		r.Approvals = append(r.Approvals, approval)
	}
	return r.ChangeRequest, nil
}

func (s *Service) readRequest(ctx context.Context, q contracts.Querier, databaseID, id string) (ChangeRequest, error) {
	r, err := scanRequest(q.QueryRowContext(ctx, requestSelect+` WHERE cr.database_id=$1 AND cr.id=$2`, databaseID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeRequest{}, requestNotFound()
	}
	if err != nil {
		return ChangeRequest{}, internal(err)
	}
	return s.renderRequest(ctx, q, r)
}

func (s *Service) GetChangeRequest(ctx context.Context, actor contracts.Principal, databaseID, requestID string) (ChangeRequest, error) {
	if err := s.require(ctx, actor, contracts.ViewDatabase, databaseID); err != nil {
		return ChangeRequest{}, err
	}
	if !contracts.ValidID(requestID) {
		return ChangeRequest{}, requestNotFound()
	}
	return s.getRequest(ctx, databaseID, requestID)
}

func (s *Service) getRequest(ctx context.Context, databaseID, id string) (ChangeRequest, error) {
	return inTx(ctx, s.store.DB, &snapshot, func(tx *sql.Tx) (ChangeRequest, error) {
		return s.readRequest(ctx, tx, databaseID, id)
	})
}

func (s *Service) listRequests(ctx context.Context, databaseID, status, migrationID string, limit, offset int) ([]ChangeRequest, error) {
	return inTx(ctx, s.store.DB, &snapshot, func(tx *sql.Tx) ([]ChangeRequest, error) {
		rows, err := tx.QueryContext(ctx, requestSelect+`
			WHERE cr.database_id=$1 AND ($2='' OR cr.status=$2) AND
				(NULLIF($3,'') IS NULL OR cr.migration_id=NULLIF($3,'')::uuid)
			ORDER BY cr.updated_at DESC, cr.id DESC LIMIT $4 OFFSET $5`, databaseID, status, migrationID, limit, offset)
		if err != nil {
			return nil, internal(err)
		}
		pending, err := collectRows(rows, scanRequest)
		if err != nil {
			return nil, err
		}
		items := make([]ChangeRequest, 0, len(pending))
		for _, r := range pending {
			item, err := s.renderRequest(ctx, tx, r)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	})
}

func (s *Service) submitRequest(ctx context.Context, actor contracts.Principal, databaseID, migrationID string, in requestInput) (ChangeRequest, error) {
	if !validText(in.Note, 0, 2000) {
		return ChangeRequest{}, invalidField("note", "Catatan maksimal 2000 karakter.")
	}
	if in.TargetExecutionID != nil && !contracts.ValidID(*in.TargetExecutionID) {
		return ChangeRequest{}, invalidField("target_execution_id", "ID eksekusi tidak valid.")
	}
	r, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (ChangeRequest, error) {
		if err := lockMigration(ctx, tx, databaseID, migrationID); err != nil {
			return ChangeRequest{}, err
		}
		m, err := readMigration(ctx, tx, databaseID, migrationID)
		if err != nil {
			return ChangeRequest{}, err
		}
		if m.openRequestID != nil {
			return ChangeRequest{}, problem("REQUEST_EXISTS", "Migrasi sudah memiliki change request terbuka.")
		}
		if in.BatchSize != nil && (m.Current.Mode != "batch" || *in.BatchSize < 1 || *in.BatchSize > s.batchSizeMax) {
			return ChangeRequest{}, invalidField("batch_size", "Ukuran batch tidak valid untuk migrasi ini.")
		}
		head, err := headVersion(ctx, tx, databaseID)
		if err != nil {
			return ChangeRequest{}, internal(err)
		}
		if err := validateRequestAction(ctx, tx, m.Migration, head, in); err != nil {
			return ChangeRequest{}, err
		}
		id := uuid.Must(uuid.NewV7()).String()
		now := s.store.now()
		_, err = tx.ExecContext(ctx, `INSERT INTO change_requests
			(id, database_id, migration_id, action, migration_revision, content_hash, head_at_request,
				target_execution_id, batch_size, note, requested_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`, id, databaseID, migrationID, in.Action,
			m.Current.Number, m.contentHash, head, in.TargetExecutionID, in.BatchSize, in.Note, actor.UserID, now)
		if err != nil {
			return ChangeRequest{}, internal(err)
		}
		return s.readRequest(ctx, tx, databaseID, id)
	})
	if err != nil {
		return ChangeRequest{}, err
	}
	s.recordRequest(ctx, actor, "request.submitted", r)
	return r, nil
}

func validateRequestAction(ctx context.Context, tx *sql.Tx, m Migration, head int64, in requestInput) error {
	if in.TargetExecutionID != nil && (in.Action != "rollback" || m.Status != "partial") {
		return invalidField("target_execution_id", "Target eksekusi hanya berlaku untuk rollback migrasi partial.")
	}
	switch in.Action {
	case "apply":
		if m.Status != "pending" {
			return problem("VALIDATION_FAILED", "Migrasi harus berstatus pending.")
		}
		for _, finding := range m.Current.Lint {
			if finding.Severity == "error" {
				return problem("VALIDATION_FAILED", "Perbaiki temuan lint sebelum mengajukan migrasi.")
			}
		}
	case "rollback":
		if m.Status == "partial" {
			if err := validatePartialRollback(ctx, tx, m, in.TargetExecutionID); err != nil {
				return err
			}
		} else if m.Status != "applied" || m.Version == nil || *m.Version != head {
			return problem("ROLLBACK_NOT_ALLOWED", "Rollback hanya tersedia untuk migrasi aktif terakhir.")
		}
		if m.Current.DownSQL == nil {
			return problem("NOT_REVERSIBLE", "Migrasi ini tidak memiliki SQL down.")
		}
	case "forward":
		if m.Status != "rolled_back" || m.Version == nil || *m.Version != head+1 {
			return problem("FORWARD_NOT_ALLOWED", "Migrasi ini tidak berada pada posisi forward berikutnya.")
		}
	default:
		return invalidField("action", "Pilih aksi apply, rollback, atau forward.")
	}
	return nil
}

func validatePartialRollback(ctx context.Context, tx *sql.Tx, m Migration, executionID *string) error {
	notAllowed := problem("ROLLBACK_NOT_ALLOWED", "Pilih eksekusi partial terakhir dari migrasi ini.")
	if executionID == nil {
		return notAllowed
	}
	var latestID, status, direction string
	err := tx.QueryRowContext(ctx, `SELECT id, status, direction FROM migration_executions
		WHERE database_id=$1 AND migration_id=$2 AND action<>'dry_run' AND status NOT IN ('cancelled','failed')
		ORDER BY number DESC LIMIT 1`, m.DatabaseID, m.ID).Scan(&latestID, &status, &direction)
	if errors.Is(err, sql.ErrNoRows) {
		return notAllowed
	}
	if err != nil {
		return internal(err)
	}
	if *executionID != latestID || status != "partial" || direction != "up" {
		return notAllowed
	}
	return nil
}

func (s *Service) cancelRequest(ctx context.Context, actor contracts.Principal, databaseID, requestID string) (ChangeRequest, error) {
	r, err := inTx(ctx, s.store.DB, nil, func(tx *sql.Tx) (ChangeRequest, error) {
		subject, err := s.store.LoadSubject(ctx, tx, requestID)
		if err != nil {
			return ChangeRequest{}, err
		}
		if subject.DatabaseID != databaseID {
			return ChangeRequest{}, requestNotFound()
		}
		if err := s.store.transitionOpenRequest(ctx, tx, requestID, "cancelled"); err != nil {
			return ChangeRequest{}, err
		}
		return s.readRequest(ctx, tx, databaseID, requestID)
	})
	if err != nil {
		return ChangeRequest{}, err
	}
	s.recordRequest(ctx, actor, "request.cancelled", r)
	return r, nil
}

func (s *Service) resetOpenRequest(ctx context.Context, tx *sql.Tx, migrationID string, revision int64, hash []byte) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `UPDATE change_requests
		SET migration_revision=$2, content_hash=$3, status='pending_review', updated_at=$4
		WHERE migration_id=$1 AND status IN ('pending_review','approved') RETURNING id`,
		migrationID, revision, hash, s.store.now()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", internal(err)
	}
	if err := s.review.DismissApprovals(ctx, tx, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) recordRequest(ctx context.Context, actor contracts.Principal, action string, r ChangeRequest) {
	s.auditChange(ctx, actor, action, r.DatabaseID, "change_request", r.ID, map[string]any{
		"migration_id": r.MigrationID, "revision": r.MigrationRevision, "action": r.Action, "status": r.Status,
	})
	s.publish(ctx, r.DatabaseID, "request.updated", r.ID, r.Status)
}
