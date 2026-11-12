package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs/jobstest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func TestMigrationReviewHTTP(t *testing.T) {
	db, dsn := appDB(t)
	const (
		databaseID = "31000000-0000-4000-8000-000000000001"
		otherID    = "31000000-0000-4000-8000-000000000002"
		sqliteID   = "31000000-0000-4000-8000-000000000003"
		editorID   = "11000000-0000-4000-8000-000000000001"
		viewerID   = "11000000-0000-4000-8000-000000000002"
		leadID     = "11000000-0000-4000-8000-000000000003"
		outsiderID = "11000000-0000-4000-8000-000000000004"
		editGroup  = "21000000-0000-4000-8000-000000000001"
		viewGroup  = "21000000-0000-4000-8000-000000000002"
		password   = "example password 123"
	)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := (auth.PasswordHasher{}).Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{editorID: "editor", viewerID: "viewer", leadID: "lead", outsiderID: "outsider"} {
		exec(`INSERT INTO users(id, username, password_hash) VALUES($1,$2,$3)`, id, name, hash)
	}
	for id, engine := range map[string]string{databaseID: "postgres", otherID: "postgres", sqliteID: "sqlite"} {
		// The registry can read driver metadata even though the target itself is unreachable.
		exec(`INSERT INTO database_connections(id, name, driver, config_encrypted) VALUES($1,$2,$3,'\x00')`, id, id, engine)
	}
	exec(`INSERT INTO groups(id, name) VALUES($1,'editors'),($2,'viewers')`, editGroup, viewGroup)
	exec(`INSERT INTO group_memberships(group_id,user_id) VALUES($1,$2),($1,$3),($4,$5)`,
		editGroup, editorID, leadID, viewGroup, viewerID)
	exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, editGroup, leadID)
	exec(`UPDATE groups SET leader_user_id=$2 WHERE id=$1`, viewGroup, viewerID)
	for _, id := range []string{databaseID, otherID, sqliteID} {
		exec(`INSERT INTO group_database_access(group_id,database_id,level) VALUES($1,$3,'edit'),($2,$3,'view')`, editGroup, viewGroup, id)
	}
	dir := t.TempDir()
	cfg := platform.Config{
		AppEnv: "test", AppOrigin: "http://localhost:8080", MetadataDatabaseURL: dsn, SessionIdleTimeout: 30 * time.Minute,
		SessionAbsoluteTimeout: 8 * time.Hour, SessionRotationInterval: 15 * time.Minute, SessionGracePeriod: 30 * time.Second,
	}
	nc, _ := jobstest.Start(t).Connect()
	sub, err := nc.SubscribeSync(jobs.EventSubject(databaseID))
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	h, close, err := newApp(t.Context(), db, cfg, runtimeConfig{
		EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		SQLiteDir:     dir, WebDir: testWeb(t), AtlasBin: filepath.Join(dir, "absent-atlas"), BatchSizeMax: 50,
	}, jobs.NewEventBus(nc))
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	type session struct {
		cookie *http.Cookie
		csrf   string
	}
	call := func(who session, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Origin", cfg.AppOrigin)
		r.Header.Set("Content-Type", "application/json")
		if who.cookie != nil {
			r.AddCookie(who.cookie)
		}
		r.Header.Set("X-CSRF-Token", who.csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(username string) session {
		type csrf struct {
			Token string `json:"csrf_token"`
		}
		w := call(session{}, "GET", "/auth/csrf", "")
		token := migrationResponse[csrf](t, w, 200).Data.Token
		w = call(session{w.Result().Cookies()[0], token}, "POST", "/auth/login", `{"username":"`+username+`","password":"`+password+`"}`)
		if w.Code != 200 {
			t.Fatalf("login as %s: %d %s", username, w.Code, w.Body.String())
		}
		cookie := w.Result().Cookies()[0]
		w = call(session{cookie, ""}, "GET", "/auth/csrf", "")
		return session{cookie, migrationResponse[csrf](t, w, 200).Data.Token}
	}
	editor, viewer, lead, outsider := login("editor"), login("viewer"), login("lead"), login("outsider")
	base, other, sqlite := "/databases/"+databaseID, "/databases/"+otherID, "/databases/"+sqliteID
	const createBody = `{"title":"  Add items  ","up_sql":"CREATE TABLE items(id integer PRIMARY KEY)","down_sql":"DROP TABLE items","mode":"normal"}`
	const applyBody = `{"action":"apply"}`
	for _, test := range []struct {
		who    session
		method string
		status int
		code   string
	}{
		{session{}, "GET", 401, "UNAUTHENTICATED"},
		{session{editor.cookie, ""}, "POST", 403, "CSRF_REJECTED"},
		{viewer, "POST", 403, "FORBIDDEN"},
		{outsider, "GET", 404, "NOT_FOUND"},
	} {
		migrationError(t, call(test.who, test.method, base+"/migrations", createBody), test.status, test.code)
	}
	migrationError(t, call(editor, "POST", base+"/migrations", `{"title":"test","up_sql":"SELECT 1","mode":"normal"}`), 400, "INVALID_REQUEST")
	migrationError(t, call(editor, "POST", base+"/migrations", strings.Replace(createBody, "CREATE TABLE items(id integer PRIMARY KEY)",
		strings.Repeat("x", 256*1024+1), 1)), 413, "PAYLOAD_TOO_LARGE")
	invalid := migrationResponse[migrations.Migration](t, call(editor, "POST", base+"/migrations",
		`{"title":"Invalid","up_sql":"SELECT FROM ;","irreversible_reason":"Fixture","mode":"normal"}`), 201).Data
	if len(invalid.Current.Lint) != 2 || invalid.Current.Lint[0].Rule != "parse_error" || invalid.Current.Lint[0].Severity != "error" {
		t.Fatalf("unexpected lint for invalid SQL: %+v", invalid.Current.Lint)
	}
	migrationError(t, call(editor, "POST", base+"/migrations/"+invalid.ID+"/requests", applyBody), 422, "VALIDATION_FAILED")

	batchBody := `{"title":"Batch","up_sql":"UPDATE public.items SET id=id WHERE id IN (SELECT id FROM public.items WHERE id > $1 AND id <= $2 ORDER BY id LIMIT $3) RETURNING id","irreversible_reason":"Fixture","mode":"batch","batch":{"schema":"public","table":"items","key_column":"id","batch_size":50,"pause_ms":0}}`
	migrationError(t, call(editor, "POST", sqlite+"/migrations", batchBody), 422, "UNSUPPORTED_MODE")
	migrationError(t, call(editor, "POST", base+"/migrations", strings.Replace(batchBody, `"batch_size":50`, `"batch_size":51`, 1)), 400, "INVALID_REQUEST")
	batch := migrationResponse[migrations.Migration](t, call(editor, "POST", base+"/migrations", batchBody), 201).Data
	if batch.Current.Batch == nil || batch.Current.Batch.BatchSize != 50 || len(batch.Current.Lint) != 1 || batch.Current.Lint[0].Rule != "irreversible" {
		t.Fatalf("unexpected batch revision: %+v", batch.Current)
	}
	migrationError(t, call(editor, "POST", base+"/migrations/"+batch.ID+"/requests", `{"action":"apply","batch_size":51}`), 400, "INVALID_REQUEST")

	m := migrationResponse[migrations.Migration](t, call(editor, "POST", base+"/migrations", createBody), 201).Data
	if m.Title != "Add items" || m.Status != "pending" || m.Current.Number != 1 || m.Current.CreatedBy.ID != editorID || m.Current.DownSQL == nil ||
		m.LatestDryRun != nil && string(m.LatestDryRun) != "null" {
		t.Fatalf("unexpected migration: %+v", m)
	}
	path := base + "/migrations/" + m.ID
	migrationError(t, call(viewer, "GET", other+"/migrations/"+m.ID, ""), 404, "NOT_FOUND")
	migrationError(t, call(viewer, "PATCH", path, `{"revision":1,"title":"x"}`), 403, "FORBIDDEN")
	migrationError(t, call(editor, "POST", path+"/requests", `{"action":"rollback"}`), 409, "ROLLBACK_NOT_ALLOWED")
	migrationError(t, call(editor, "POST", path+"/requests", `{"action":"forward"}`), 409, "FORWARD_NOT_ALLOWED")
	r := migrationResponse[migrations.ChangeRequest](t, call(editor, "POST", path+"/requests", applyBody), 201).Data
	if r.Status != "pending_review" || r.MigrationRevision != 1 || r.ContentHash != m.Current.ContentHash || len(r.Approvals) != 2 ||
		r.Approvals[0].Leader == nil || r.Approvals[0].Decision != "pending" || r.RequestedBy.ID != editorID {
		t.Fatalf("unexpected submitted request: %+v", r)
	}
	reqPath := base + "/change-requests/" + r.ID
	migrationError(t, call(editor, "POST", path+"/requests", applyBody), 409, "REQUEST_EXISTS")
	migrationError(t, call(editor, "POST", reqPath+"/approve", `{}`), 403, "NOT_LEADER")
	migrationError(t, call(viewer, "POST", other+"/change-requests/"+r.ID+"/approve", `{}`), 404, "NOT_FOUND")
	migrationError(t, call(viewer, "GET", other+"/change-requests/"+r.ID, ""), 404, "NOT_FOUND")

	approve := func(who session, status string) migrations.ChangeRequest {
		t.Helper()
		approved := migrationResponse[migrations.ChangeRequest](t, call(who, "POST", reqPath+"/approve", `{}`), 200).Data
		if approved.Status != status {
			t.Fatalf("approval status: got %s, want %s", approved.Status, status)
		}
		return approved
	}
	first := approve(viewer, "pending_review")
	if first.Approvals[0].Decision != "pending" || first.Approvals[1].Decision != "approved" {
		t.Fatalf("first leader should only approve their group: %+v", first.Approvals)
	}
	approve(lead, "approved")
	foundEvent := false
	for range 10 {
		msg, err := sub.NextMsg(time.Second)
		if err != nil {
			t.Fatal("approved request event was not published:", err)
		}
		var event struct {
			Type   string `json:"type"`
			ID     string `json:"resource_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "request.updated" && event.ID == r.ID && event.Status == "approved" {
			foundEvent = true
			break
		}
	}
	if !foundEvent {
		t.Fatal("approved request event was not published")
	}

	renamed := migrationResponse[migrations.Migration](t, call(editor, "PATCH", path, `{"revision":1,"title":"Items"}`), 200).Data
	if renamed.Title != "Items" || renamed.Current.Number != 1 || renamed.OpenRequest == nil || renamed.OpenRequest.Status != "approved" {
		t.Fatalf("title-only edit changed review or revision: %+v", renamed)
	}
	// Competing SQL changes must serialize and only one can create revision 2.
	var edits [2]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	for i, sql := range []string{"SELECT 2", "SELECT 3"} {
		wg.Go(func() { edits[i] = call(editor, "PATCH", path, `{"revision":1,"up_sql":"`+sql+`"}`) })
	}
	wg.Wait()
	if edits[0].Code != 200 {
		edits[0], edits[1] = edits[1], edits[0]
	}
	revised := migrationResponse[migrations.Migration](t, edits[0], 200).Data
	migrationError(t, edits[1], 409, "REVISION_CONFLICT")
	if revised.Current.Number != 2 || revised.Current.ContentHash == m.Current.ContentHash || revised.OpenRequest == nil ||
		revised.OpenRequest.ID != r.ID || revised.OpenRequest.Status != "pending_review" || revised.OpenRequest.MigrationRevision != 2 ||
		revised.OpenRequest.ContentHash != revised.Current.ContentHash {
		t.Fatalf("SQL change did not reset the request for the new revision: %+v", revised)
	}
	for _, decision := range revised.OpenRequest.Approvals {
		if decision.Decision != "pending" {
			t.Fatalf("previous approval survived the new revision: %+v", decision)
		}
	}
	var dismissed int
	if err := db.QueryRow(`SELECT count(*) FROM change_request_approvals WHERE request_id=$1 AND dismissed_at IS NOT NULL`, r.ID).Scan(&dismissed); err != nil || dismissed != 2 {
		t.Fatalf("expected two dismissed approval records, got %d: %v", dismissed, err)
	}
	revisions := migrationResponse[[]migrations.Revision](t, call(viewer, "GET", path+"/revisions", ""), 200).Data
	if len(revisions) != 2 || revisions[0].Number != 2 || revisions[1].ContentHash != m.Current.ContentHash {
		t.Fatalf("revision history: %+v", revisions)
	}
	page := migrationResponse[[]migrations.Migration](t, call(viewer, "GET", base+"/migrations?status=pending&limit=1", ""), 200)
	if len(page.Data) != 1 || page.Page == nil || !page.Page.HasMore {
		t.Fatalf("migration pagination: %+v", page)
	}

	migrationError(t, call(lead, "POST", reqPath+"/reject", `{}`), 400, "INVALID_REQUEST")
	rejected := migrationResponse[migrations.ChangeRequest](t, call(lead, "POST", reqPath+"/reject", `{"comment":"Tambahkan indeks."}`), 200).Data
	if rejected.Status != "rejected" || rejected.Approvals[0].Comment == nil || *rejected.Approvals[0].Comment != "Tambahkan indeks." {
		t.Fatalf("unexpected rejected request: %+v", rejected)
	}
	migrationError(t, call(editor, "POST", reqPath+"/cancel", `{}`), 409, "REQUEST_NOT_OPEN")
	items := migrationResponse[[]migrations.ChangeRequest](t,
		call(viewer, "GET", base+"/change-requests?status=rejected&migration_id="+m.ID, ""), 200).Data
	if len(items) != 1 || items[0].ID != rejected.ID {
		t.Fatalf("filtered requests: %+v", items)
	}
	retry := migrationResponse[migrations.ChangeRequest](t, call(editor, "POST", path+"/requests", applyBody), 201).Data
	cancelPath := base + "/change-requests/" + retry.ID + "/cancel"
	migrationError(t, call(viewer, "POST", cancelPath, `{}`), 403, "FORBIDDEN")
	cancelled := migrationResponse[migrations.ChangeRequest](t, call(editor, "POST", cancelPath, `{}`), 200).Data
	if cancelled.Status != "cancelled" {
		t.Fatalf("unexpected cancel status: %s", cancelled.Status)
	}
	exec(`UPDATE migrations SET locked_at=now(), locked_revision=current_revision WHERE id=$1`, m.ID)
	migrationError(t, call(editor, "PATCH", path, `{"revision":2,"up_sql":"SELECT 4"}`), 409, "MIGRATION_LOCKED")
	locked := migrationResponse[migrations.Migration](t, call(editor, "PATCH", path, `{"revision":2,"description":"Locked revision"}`), 200).Data
	if !locked.Locked || locked.Current.Number != 2 || locked.Description != "Locked revision" || locked.OpenRequest != nil {
		t.Fatalf("locked migration metadata edit: %+v", locked)
	}

	// Model a committed apply and rollback in metadata to exercise both positional
	// checks before the worker is integrated.
	apply := migrationResponse[migrations.ChangeRequest](t, call(editor, "POST", path+"/requests", applyBody), 201).Data
	commitHistory := func(requestID, executionID, action, direction string, sequence, head int, reverts *string) {
		t.Helper()
		exec(`UPDATE change_requests SET status='executed', closed_at=now() WHERE id=$1`, requestID)
		exec(`INSERT INTO migration_executions
			(id,database_id,number,migration_id,change_request_id,action,direction,mode,migration_revision,content_hash,
			 status,max_attempts,version,reverts_execution_id,chain_root_id,enqueued_by,enqueued_by_name,enqueued_at)
			SELECT $2,database_id,$3,migration_id,id,action,$4,'normal',migration_revision,content_hash,
			 'succeeded',3,1,$5,$2,requested_by,'editor',now()
			FROM change_requests WHERE id=$1`, requestID, executionID, sequence, direction, reverts)
		exec(`INSERT INTO migration_history
			(database_id,seq,execution_id,migration_id,migration_revision,action,version,head_after,reverts_execution_id,
			 sql_executed,sql_hash,mode,actor_id,actor_name,committed_at)
			SELECT e.database_id,e.number,e.id,e.migration_id,e.migration_revision,e.action,e.version,$2,e.reverts_execution_id,
			 CASE WHEN e.direction='up' THEN r.up_sql ELSE r.down_sql END,r.content_hash,e.mode,e.enqueued_by,e.enqueued_by_name,now()
			FROM migration_executions e JOIN migration_revisions r ON (r.migration_id,r.revision)=(e.migration_id,e.migration_revision)
			WHERE e.id=$1`, executionID, head)
		if action == "apply" {
			exec(`UPDATE migrations SET status='applied', version=1 WHERE id=$1`, m.ID)
		} else {
			exec(`UPDATE migrations SET status='rolled_back' WHERE id=$1`, m.ID)
		}
	}
	const applyExecution = "41000000-0000-4000-8000-000000000001"
	const rollbackExecution = "41000000-0000-4000-8000-000000000002"
	commitHistory(apply.ID, applyExecution, "apply", "up", 1, 1, nil)
	rollback := migrationResponse[migrations.ChangeRequest](t,
		call(editor, "POST", path+"/requests", `{"action":"rollback"}`), 201).Data
	if rollback.Action != "rollback" || rollback.HeadAtRequest != 1 || rollback.MigrationRevision != 2 {
		t.Fatalf("rollback request must use the locked head revision: %+v", rollback)
	}
	commitHistory(rollback.ID, rollbackExecution, "rollback", "down", 2, 0, new(applyExecution))
	forward := migrationResponse[migrations.ChangeRequest](t,
		call(editor, "POST", path+"/requests", `{"action":"forward"}`), 201).Data
	if forward.Action != "forward" || forward.HeadAtRequest != 0 || forward.MigrationRevision != 2 {
		t.Fatalf("forward request must use the next locked revision: %+v", forward)
	}
}

type migrationEnvelope[T any] struct {
	Data T              `json:"data"`
	Page *platform.Page `json:"page"`
}

func migrationResponse[T any](t *testing.T, w *httptest.ResponseRecorder, status int) migrationEnvelope[T] {
	t.Helper()
	var response migrationEnvelope[T]
	if err := json.Unmarshal(w.Body.Bytes(), &response); w.Code != status || err != nil {
		t.Fatalf("wanted HTTP %d, got %d %s (decode: %v)", status, w.Code, w.Body.String(), err)
	}
	return response
}

func migrationError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); w.Code != status || err != nil || response.Error.Code != code {
		t.Fatalf("wanted HTTP %d %s, got %d %s (decode: %v)", status, code, w.Code, w.Body.String(), err)
	}
}
