package review_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/review"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

// subjects stands in for the migrations module: it serves one stored request and keeps its status.
type subjects struct {
	subject contracts.ReviewSubject
	applied []string
}

func (f *subjects) LoadSubject(_ context.Context, q contracts.Querier, id string) (contracts.ReviewSubject, error) {
	if id != f.subject.RequestID {
		return contracts.ReviewSubject{}, &contracts.DomainError{Code: "NOT_FOUND", Message: "Change request tidak ditemukan."}
	}
	s := f.subject
	if err := q.QueryRowContext(context.Background(), `SELECT status FROM change_requests WHERE id=$1`, id).Scan(&s.Status); err != nil {
		return contracts.ReviewSubject{}, err
	}
	return s, nil
}

func (f *subjects) ApplyReviewState(ctx context.Context, tx *sql.Tx, id, status string) error {
	f.applied = append(f.applied, status)
	_, err := tx.ExecContext(ctx, `UPDATE change_requests SET status=$2, updated_at=now(),
		closed_at=CASE WHEN $2='rejected' THEN now() END WHERE id=$1`, id, status)
	return err
}

type fixture struct {
	db       *sql.DB
	subjects *subjects
	audit    *testkit.AuditRecorder
	svc      *review.Service
}

func newFixture(t *testing.T, databaseID string) fixture {
	db := reviewDB(t)
	f := fixture{db: db, subjects: &subjects{subject: newRequest(t, db, databaseID, 1)}, audit: &testkit.AuditRecorder{}}
	f.svc = review.NewService(db, f.subjects, f.audit, nil, testkit.NewClock(time.Now()))
	return f
}

func principal(id, name string) contracts.Principal {
	return contracts.Principal{UserID: id, Username: name}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	domain, ok := err.(*contracts.DomainError)
	if !ok || domain.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestApproveByEveryLeaderApprovesTheRequest(t *testing.T) {
	f := newFixture(t, dbShared)
	if err := f.svc.Approve(t.Context(), principal(userViewer, "viewer"), dbShared, requestID, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Approve(t.Context(), principal(userLead, "lead"), dbShared, requestID, new(" Aman. ")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.subjects.applied, ",") != "pending_review,approved" {
		t.Fatal("status must follow each evaluation", f.subjects.applied)
	}
	var comment string
	if err := f.db.QueryRow(`SELECT comment FROM change_request_approvals WHERE approver_id=$1`, userLead).Scan(&comment); err != nil || comment != "Aman." {
		t.Fatal("approval comment", comment, err)
	}
	events := f.audit.Events()
	if len(events) != 2 || events[1].Action != "request.approved" || events[1].Details["status"] != "approved" ||
		*events[1].DatabaseID != dbShared || events[1].ResourceID != requestID {
		t.Fatal("approvals must be audited after commit", events)
	}
	// Already approved: nothing left for this leader to decide.
	requireCode(t, f.svc.Approve(t.Context(), principal(userLead, "lead"), dbShared, requestID, nil), "NOT_LEADER")
}

func TestOneApprovalCoversEveryGroupTheLeaderLeads(t *testing.T) {
	f := newFixture(t, dbShared)
	if _, err := f.db.Exec(`INSERT INTO group_database_access(group_id, database_id, level) VALUES($1, $2, 'view')`, groupOps, dbShared); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Approve(t.Context(), principal(userLead, "lead"), dbShared, requestID, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM change_request_approvals WHERE approver_id=$1`, userLead).Scan(&count); err != nil || count != 2 {
		t.Fatal("one click must decide both groups the leader leads", count, err)
	}
	if got := f.audit.Events()[0].Details["group_ids"].([]string); len(got) != 2 {
		t.Fatal("audit must list both groups", got)
	}
}

func TestRejectNeedsACommentAndClosesTheRequest(t *testing.T) {
	f := newFixture(t, dbShared)
	for _, comment := range []string{"", "   ", strings.Repeat("x", 2001)} {
		requireCode(t, f.svc.Reject(t.Context(), principal(userLead, "lead"), dbShared, requestID, comment), "INVALID_REQUEST")
	}
	if err := f.svc.Reject(t.Context(), principal(userLead, "lead"), dbShared, requestID, "Tambahkan indeks."); err != nil {
		t.Fatal(err)
	}
	if f.subjects.applied[0] != "rejected" || f.audit.Events()[0].Action != "request.rejected" {
		t.Fatal("rejection must close the request", f.subjects.applied)
	}
	requireCode(t, f.svc.Approve(t.Context(), principal(userViewer, "viewer"), dbShared, requestID, nil), "REQUEST_NOT_OPEN")
}

func TestOnlyCurrentLeadersMayDecide(t *testing.T) {
	f := newFixture(t, dbShared)
	// The editor can edit the database but leads no group; an admin gets no bypass either.
	requireCode(t, f.svc.Approve(t.Context(), principal(userEditor, "editor"), dbShared, requestID, nil), "NOT_LEADER")
	requireCode(t, f.svc.Approve(t.Context(), contracts.Principal{UserID: userEditor, IsPlatformAdmin: true}, dbShared, requestID, nil), "NOT_LEADER")
	requireCode(t, f.svc.Approve(t.Context(), principal(userViewer, "viewer"), dbOps, requestID, nil), "NOT_FOUND")
	requireCode(t, f.svc.Approve(t.Context(), principal(userViewer, "viewer"), dbShared, migrationID, nil), "NOT_FOUND")
	if len(f.subjects.applied) != 0 || len(f.audit.Events()) != 0 {
		t.Fatal("refused decisions must change nothing")
	}
}

func TestNewLeaderReplacesTheFormerLeadersDecision(t *testing.T) {
	f := newFixture(t, dbShared)
	if err := f.svc.Approve(t.Context(), principal(userViewer, "viewer"), dbShared, requestID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, groupView, userEditor); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Approve(t.Context(), principal(userEditor, "editor"), dbShared, requestID, nil); err != nil {
		t.Fatal("the former leader's active approval must not block the new leader", err)
	}
	var active, dismissed int
	if err := f.db.QueryRow(`SELECT count(*) FILTER (WHERE dismissed_at IS NULL), count(*) FILTER (WHERE dismissed_at IS NOT NULL)
		FROM change_request_approvals WHERE group_id=$1`, groupView).Scan(&active, &dismissed); err != nil || active != 1 || dismissed != 1 {
		t.Fatal("the former decision must be dismissed, not deleted", active, dismissed, err)
	}
}

func TestHandlerRoutesAndResponses(t *testing.T) {
	f := newFixture(t, dbShared)
	access := testkit.NewFixtureAccessChecker()
	var actor contracts.Principal
	mux := http.NewServeMux()
	review.RegisterRoutes(mux, review.NewHandler(f.svc, access, func(context.Context) (contracts.Principal, bool) {
		return actor, actor.UserID != ""
	}, func(_ context.Context, _ contracts.Principal, databaseID, id string) (any, error) {
		return map[string]string{"id": id, "database_id": databaseID}, nil
	}))
	call := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/databases/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}
	base := dbShared + "/change-requests/" + requestID
	if w := call(base+"/approve", `{}`); w.Code != 401 {
		t.Fatal("anonymous", w.Code)
	}
	actor = principal(testkit.UserNone, "outsider")
	if w := call(base+"/approve", `{}`); w.Code != 404 {
		t.Fatal("no access must hide the database", w.Code, w.Body.String())
	}
	actor = principal(testkit.UserEdit, "editor")
	if w := call(base+"/approve", `{}`); w.Code != 403 || !strings.Contains(w.Body.String(), "NOT_LEADER") {
		t.Fatal("an editor who leads nothing", w.Code, w.Body.String())
	}
	actor = principal(testkit.UserView, "viewer")
	if w := call(base+"/reject", `{"comment":""}`); w.Code != 400 {
		t.Fatal("empty rejection", w.Code, w.Body.String())
	}
	if w := call(base+"/approve", `{"decision":"forged"}`); w.Code != 400 {
		t.Fatal("unknown fields", w.Code)
	}
	w := call(base+"/approve", `{"comment":"Aman."}`)
	var got struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got.Data["id"] != requestID || got.Data["database_id"] != dbShared {
		t.Fatal("approve must answer with the rendered change request", w.Code, w.Body.String())
	}
}
