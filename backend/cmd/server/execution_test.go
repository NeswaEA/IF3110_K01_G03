package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/access"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/audit"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/jobs/jobstest"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/review"
)

func TestQueuedNormalMigrations(t *testing.T) {
	for _, engine := range []database.Driver{database.Postgres, database.SQLite} {
		t.Run(string(engine), func(t *testing.T) {
			f := newExecutionFixture(t, engine)
			create := f.approved(t, "CREATE TABLE execution_items (id integer PRIMARY KEY, value integer NOT NULL)", "DROP TABLE execution_items")
			insert := f.approved(t, "INSERT INTO execution_items VALUES (1, 10)", "DELETE FROM execution_items WHERE id=1")
			e1 := f.execute(t, create, 202)
			if again := f.execute(t, create, 200); again.ID != e1.ID || again.Number != 1 {
				t.Fatalf("duplicate execute produced another job: %+v", again)
			}
			e2 := f.execute(t, insert, 202)
			if e1.Status != "queued" || e1.Attempt != 0 || e2.Number != 2 {
				t.Fatalf("unexpected queue order: %+v, %+v", e1, e2)
			}
			f.startWorker(t)
			f.await(t, e1.ID, "succeeded")
			f.await(t, e2.ID, "succeeded")
			f.assertTargetValue(t, 10)

			failing := f.approved(t, "UPDATE execution_items SET value=99 WHERE id=1; INSERT INTO missing_table VALUES (1)", "SELECT 1")
			e3 := f.execute(t, failing, 202)
			failed := f.await(t, e3.ID, "failed")
			if failed.Error == nil || failed.Error.Code != "SQL_EXECUTION_FAILED" || failed.HistorySeq != nil {
				t.Fatalf("failed execution: %+v", failed)
			}
			f.assertTargetValue(t, 10)
			m := migrationResponse[migrations.Migration](t, f.call("GET", "/migrations/"+failing.MigrationID, ""), 200).Data
			if !m.Locked || m.Status != "pending" {
				t.Fatalf("SQL failure should retain the locked revision without advancing the migration: %+v", m)
			}
			migrationError(t, f.call("POST", "/migrations/"+create.MigrationID+"/requests", `{"action":"rollback"}`), 409, "ROLLBACK_NOT_ALLOWED")

			rollback := f.request(t, insert.MigrationID, "rollback")
			e4 := f.execute(t, rollback, 202)
			rolled := f.await(t, e4.ID, "succeeded")
			if rolled.RevertsExecution == nil || *rolled.RevertsExecution != e2.ID {
				t.Fatalf("rollback did not link the previous execution: %+v", rolled)
			}
			head := migrationResponse[migrations.DatabaseHead](t, f.call("GET", "/head", ""), 200).Data
			if head.Version != 1 || head.MigrationID == nil || *head.MigrationID != create.MigrationID {
				t.Fatalf("head after rollback: %+v", head)
			}
			e5 := f.execute(t, f.request(t, insert.MigrationID, "forward"), 202)
			f.await(t, e5.ID, "succeeded")
			f.assertTargetValue(t, 10)
			entries := migrationResponse[[]migrations.TimelineEntry](t, f.call("GET", "/history?hide_rollback=true", ""), 200).Data
			if len(entries) != 3 || entries[0].Execution.ID != e5.ID || entries[1].Execution.ID != e3.ID ||
				entries[1].Committed != nil || entries[2].Execution.ID != e1.ID || entries[2].Committed == nil {
				t.Fatalf("filtered history: %+v", entries)
			}

			recovering := f.approved(t, "UPDATE execution_items SET value=value+1 WHERE id=1", "UPDATE execution_items SET value=value-1 WHERE id=1")
			f.exec(t, `CREATE FUNCTION test_reject_history() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'test mirror failure'; END $$`)
			f.exec(t, `CREATE TRIGGER test_reject_history BEFORE INSERT ON migration_history FOR EACH ROW EXECUTE FUNCTION test_reject_history()`)
			e6 := f.execute(t, recovering, 202)
			f.eventually(t, func() bool {
				var value int
				return f.target.QueryRow("SELECT value FROM execution_items WHERE id=1").Scan(&value) == nil && value == 11
			})
			f.eventually(t, func() bool {
				select {
				case attempt := <-f.deliveries:
					return attempt.id == e6.ID && !attempt.result.Ack
				default:
					return false
				}
			})
			f.exec(t, "DROP TRIGGER test_reject_history ON migration_history")
			recovered := f.await(t, e6.ID, "succeeded")
			if recovered.Attempt != 1 || recovered.HistorySeq == nil {
				t.Fatalf("expected recovery after the final attempt: %+v", recovered)
			}
			f.assertTargetValue(t, 11)
			duplicate := f.worker.Handle(t.Context(), contracts.JobMessage{ExecutionID: e6.ID, DatabaseID: f.targetID})
			if !duplicate.Ack || duplicate.Reason != "duplicate" {
				t.Fatalf("completed job was not acknowledged as a duplicate: %+v", duplicate)
			}

			diffBody := `{"title":"Stale diff","mode":"normal","up_sql":"SELECT 1","down_sql":"SELECT 1","source_fingerprint":"sha256:` +
				strings.Repeat("0", 64) + `"}`
			diff := migrationResponse[migrations.Migration](t, f.call("POST", "/migrations", diffBody), 201).Data
			e7 := f.execute(t, f.request(t, diff.ID, "apply"), 202)
			rejected := f.await(t, e7.ID, "failed")
			if rejected.Error == nil || rejected.Error.Code != "SCHEMA_CONFLICT" {
				t.Fatalf("stale source fingerprint: %+v", rejected)
			}
			current := migrationResponse[migrations.Migration](t, f.call("GET", "/migrations/"+diff.ID, ""), 200).Data
			if current.Locked {
				t.Fatal("a revalidation failure locked the revision")
			}

			historyTable := "_platform.migration_history"
			if engine == database.SQLite {
				historyTable = "_platform_migration_history"
			}
			if _, err := f.target.Exec("UPDATE " + historyTable + " SET seq=seq"); err == nil {
				t.Fatal("target history accepted an update")
			}
			if _, err := f.target.Exec("DELETE FROM " + historyTable); err == nil {
				t.Fatal("target history accepted a delete")
			}
		})
	}
}

func TestReconcileUnblocksNextExecution(t *testing.T) {
	f := newExecutionFixture(t, database.SQLite)
	unknown := f.approved(t, "CREATE TABLE must_not_run (id integer)", "DROP TABLE must_not_run")
	first := f.execute(t, unknown, 202)
	f.exec(t, `UPDATE migration_executions SET status='outcome_unknown', attempt=1,
		error_code='TARGET_UNAVAILABLE', finished_at=now() WHERE id=$1`, first.ID)

	next := f.approved(t, "CREATE TABLE next_execution (id integer)", "DROP TABLE next_execution")
	second := f.execute(t, next, 202)
	if second.Status != "held" {
		t.Fatalf("expected held execution, got %+v", second)
	}
	head := migrationResponse[migrations.DatabaseHead](t, f.call("GET", "/head", ""), 200).Data
	if !head.Blocked || head.BlockingExecutionID == nil || *head.BlockingExecutionID != first.ID {
		t.Fatalf("database did not report the blocking execution: %+v", head)
	}
	res := f.call("POST", "/executions/"+first.ID+"/reconcile", `{}`)
	if e := migrationResponse[migrations.Execution](t, res, 202).Data; e.ID != first.ID || e.Status != "queued" {
		t.Fatalf("unexpected reconcile job: %+v", e)
	}
	f.startWorker(t)
	reconciled := f.await(t, first.ID, "failed")
	if reconciled.Error == nil || reconciled.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Fatalf("expected a failed job without commit evidence: %+v", reconciled)
	}
	f.await(t, second.ID, "succeeded")
	var found int
	if err := f.target.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='must_not_run'").Scan(&found); err != nil || found != 0 {
		t.Fatalf("reconciliation ran user SQL: count=%d (%v)", found, err)
	}
	head = migrationResponse[migrations.DatabaseHead](t, f.call("GET", "/head", ""), 200).Data
	if head.Blocked || head.Version != 1 {
		t.Fatalf("expected a committed, unblocked head: %+v", head)
	}
}

func TestWorkerRevalidatesQueuedExecution(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*testing.T, executionFixture)
	}{
		{"access", "ACCESS_REVOKED", func(t *testing.T, f executionFixture) {
			f.exec(t, `UPDATE group_database_access SET level='view' WHERE database_id=$1`, f.targetID)
		}},
		{"approval", "APPROVAL_REVOKED", func(t *testing.T, f executionFixture) {
			const group = "22000000-0000-4000-8000-000000000002"
			f.exec(t, `INSERT INTO groups (id,name) VALUES ($1,'new readers')`, group)
			f.exec(t, `INSERT INTO group_memberships (group_id,user_id) SELECT $1,id FROM users WHERE username='editor'`, group)
			f.exec(t, `UPDATE groups SET leader_user_id=(SELECT id FROM users WHERE username='editor') WHERE id=$1`, group)
			f.exec(t, `INSERT INTO group_database_access (group_id,database_id,level) VALUES ($1,$2,'view')`, group, f.targetID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecutionFixture(t, database.SQLite)
			req := f.approved(t, "CREATE TABLE must_not_run (id integer)", "DROP TABLE must_not_run")
			e := f.execute(t, req, 202)
			tc.change(t, f)
			f.startWorker(t)
			failed := f.await(t, e.ID, "failed")
			if failed.Error == nil || failed.Error.Code != tc.code {
				t.Fatalf("expected %s, got %+v", tc.code, failed.Error)
			}
			current := migrationResponse[migrations.Migration](t, f.call("GET", "/migrations/"+req.MigrationID, ""), 200).Data
			if current.Locked {
				t.Fatal("worker locked the migration despite a revalidation failure")
			}
			var count int
			if err := f.target.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='must_not_run'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("worker ran SQL despite a revalidation failure: count=%d (%v)", count, err)
			}
		})
	}
}

type delivery struct {
	id     string
	result contracts.JobResult
}

type observedExecutor struct {
	executor   *migrations.Executor
	deliveries chan<- delivery
}

func (o observedExecutor) Handle(ctx context.Context, job contracts.JobMessage) contracts.JobResult {
	result := o.executor.Handle(ctx, job)
	select {
	case o.deliveries <- delivery{job.ExecutionID, result}:
	default:
	}
	return result
}

type executionFixture struct {
	meta, target *sql.DB
	targetID     string
	call         func(method, path, body string) *httptest.ResponseRecorder
	startWorker  func(*testing.T)
	worker       *migrations.Executor
	deliveries   chan delivery
}

func newExecutionFixture(t *testing.T, engine database.Driver) executionFixture {
	t.Helper()
	meta, dsn := appDB(t)
	const targetID = "32000000-0000-4000-8000-000000000001"
	const userID = "12000000-0000-4000-8000-000000000001"
	const groupID = "22000000-0000-4000-8000-000000000001"
	f := executionFixture{meta: meta, targetID: targetID, deliveries: make(chan delivery, 128)}
	dir := t.TempDir()
	var targetConfig any
	var err error
	if engine == database.Postgres {
		f.target, targetConfig = isolatedExecutionPostgres(t, dsn)
	} else {
		f.target, err = sql.Open("sqlite3", filepath.Join(dir, "execution.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.target.Close() })
		if err := f.target.Ping(); err != nil {
			t.Fatal(err)
		}
		targetConfig = connections.SQLiteConfig{File: "execution.sqlite"}
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cipher, err := connections.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Seal(plain, []byte(targetID))
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO database_connections (id,name,driver,config_encrypted,migration_ready)
		VALUES ($1,'execution fixture',$2,$3,true)`, targetID, engine, encrypted)
	f.exec(t, `INSERT INTO users(id,username,password_hash) VALUES ($1,'editor','$argon2id$unused')`, userID)
	f.exec(t, `INSERT INTO groups(id,name) VALUES ($1,'editors')`, groupID)
	f.exec(t, `INSERT INTO group_memberships(group_id,user_id) VALUES ($1,$2)`, groupID, userID)
	f.exec(t, `UPDATE groups SET leader_user_id=$2 WHERE id=$1`, groupID, userID)
	f.exec(t, `INSERT INTO group_database_access(group_id,database_id,level) VALUES ($1,$2,'edit')`, groupID, targetID)

	clock := platform.SystemClock{}
	options := auth.DefaultSessionOptions()
	sessions, err := auth.NewSessionStore(meta, clock, options)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := meta.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	session, err := sessions.CreateInTx(t.Context(), tx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	nc, js := jobstest.Start(t).Connect()
	if err := jobs.EnsureStream(t.Context(), js); err != nil {
		t.Fatal(err)
	}
	events := jobs.NewEventBus(nc)
	relay := jobs.NewRelay(meta, js, jobs.RelayOptions{PollInterval: 20 * time.Millisecond})
	cfg := platform.Config{AppEnv: "test", AppOrigin: "http://localhost:8080", MetadataDatabaseURL: dsn,
		SessionIdleTimeout: options.IdleTimeout, SessionAbsoluteTimeout: options.AbsoluteTimeout,
		SessionRotationInterval: options.RotationInterval, SessionGracePeriod: options.GracePeriod}
	app, closeApp, err := newApp(t.Context(), meta, cfg, runtimeConfig{EncryptionKey: key, SQLiteDir: dir, WebDir: testWeb(t),
		AtlasBin: filepath.Join(dir, "absent-atlas"), MaxAttempts: 1, NotifyJobs: relay.Notify}, events)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeApp)
	f.call = func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/databases/"+targetID+path, strings.NewReader(body))
		r.Header.Set("Origin", cfg.AppOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", session.CSRFToken)
		r.AddCookie(&http.Cookie{Name: "session", Value: session.Token})
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}

	registry := connections.NewRegistry(meta, cipher, connections.RegistryOptions{SQLiteDir: dir})
	t.Cleanup(registry.Close)
	target, release, err := registry.Acquire(t.Context(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	err = (migrations.HistoryInitializer{}).EnsureReady(t.Context(), target)
	release()
	if err != nil {
		t.Fatal(err)
	}
	store := migrations.NewStore(meta, clock)
	auditLog := audit.NewRecorder(slog.Default())
	reviews := review.NewService(meta, store, auditLog, events, clock)
	svc := migrations.NewService(store, registry, access.NewPolicy(meta), reviews, auditLog, events,
		migrations.ServiceOptions{Enqueuer: jobs.NewOutbox(meta)})
	inspector := database.DispatchInspector{ByDriver: map[database.Driver]database.Inspector{
		database.Postgres: postgres.Inspector{}, database.SQLite: sqlite.Inspector{},
	}}
	lockers := map[database.Driver]database.MigrationLocker{database.Postgres: postgres.Locker{}, database.SQLite: sqlite.Locker{Metadata: meta}}
	f.worker = migrations.NewExecutor(svc, registry, inspector, lockers, migrations.ExecutorOptions{
		WorkerID: "test-worker", HeartbeatInterval: 50 * time.Millisecond, OrderingDelay: 20 * time.Millisecond,
		RetryBaseDelay: 60 * time.Millisecond, RetryMaxDelay: 150 * time.Millisecond,
	})
	f.startWorker = func(t *testing.T) {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		errs := make(chan error, 2)
		consumer := jobs.NewConsumer(js, observedExecutor{f.worker, f.deliveries}, jobs.ConsumerOptions{Concurrency: 2, AckWait: 3 * time.Second, FetchWait: 150 * time.Millisecond})
		go func() { errs <- relay.Run(ctx) }()
		go func() { errs <- consumer.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			for range 2 {
				select {
				case err := <-errs:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("test worker did not stop")
				}
			}
		})
	}
	return f
}

func isolatedExecutionPostgres(t *testing.T, metadataDSN string) (*sql.DB, connections.PostgresConfig) {
	t.Helper()
	admin, err := sql.Open("pgx", metadataDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := "exec_" + strings.ToLower(rand.Text())
	runner, editor := name+"_runner", name+"_editor"
	const password = "execution-fixture-password"
	ident := postgres.QuoteIdent
	create := func(query, cleanup string) {
		t.Helper()
		if _, err := admin.Exec(query); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := admin.Exec(cleanup); err != nil {
				t.Error("cleanup of isolated execution target failed", err)
			}
		})
	}
	create("CREATE ROLE "+ident(editor)+" NOLOGIN", "DROP ROLE "+ident(editor))
	create("CREATE ROLE "+ident(runner)+" LOGIN PASSWORD '"+password+"'", "DROP ROLE "+ident(runner))
	if _, err := admin.Exec("GRANT " + ident(editor) + " TO " + ident(runner)); err != nil {
		t.Fatal(err)
	}
	create("CREATE DATABASE "+ident(name)+" OWNER "+ident(runner), "DROP DATABASE "+ident(name)+" WITH (FORCE)")
	u, err := url.Parse(metadataDSN)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal("test metadata database URL needs a TCP port")
	}
	cfg := connections.PostgresConfig{Host: u.Hostname(), Port: port, Database: name, Username: runner, Password: password,
		SSLMode: u.Query().Get("sslmode"), EditorRole: editor}
	u.Path, u.User = "/"+name, url.UserPassword(runner, password)
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("GRANT USAGE, CREATE ON SCHEMA public TO " + ident(editor)); err != nil {
		t.Fatal(err)
	}
	return db, cfg
}

func (f executionFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.meta.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f executionFixture) approved(t *testing.T, up, down string) migrations.ChangeRequest {
	t.Helper()
	body, err := json.Marshal(map[string]string{"title": "Execution test", "mode": "normal", "up_sql": up, "down_sql": down})
	if err != nil {
		t.Fatal(err)
	}
	m := migrationResponse[migrations.Migration](t, f.call("POST", "/migrations", string(body)), 201).Data
	return f.request(t, m.ID, "apply")
}

func (f executionFixture) request(t *testing.T, id, action string) migrations.ChangeRequest {
	t.Helper()
	req := migrationResponse[migrations.ChangeRequest](t,
		f.call("POST", "/migrations/"+id+"/requests", `{"action":"`+action+`"}`), 201).Data
	path := "/change-requests/" + req.ID
	migrationError(t, f.call("POST", path+"/execute", `{}`), 409, "APPROVAL_REQUIRED")
	return migrationResponse[migrations.ChangeRequest](t, f.call("POST", path+"/approve", `{}`), 200).Data
}

func (f executionFixture) execute(t *testing.T, r migrations.ChangeRequest, status int) migrations.Execution {
	t.Helper()
	response := f.call("POST", "/change-requests/"+r.ID+"/execute", `{}`)
	e := migrationResponse[migrations.Execution](t, response, status).Data
	if response.Header().Get("Location") != "/api/v1/databases/"+f.targetID+"/executions/"+e.ID {
		t.Fatalf("unexpected execution Location: %s", response.Header().Get("Location"))
	}
	return e
}

func (f executionFixture) await(t *testing.T, id, status string) migrations.Execution {
	t.Helper()
	var e migrations.Execution
	f.eventually(t, func() bool {
		e = migrationResponse[migrations.Execution](t, f.call("GET", "/executions/"+id, ""), 200).Data
		if (e.Status == "failed" || e.Status == "outcome_unknown") && status != e.Status {
			t.Fatalf("execution %s: wanted %s, got %s (%+v)", id, status, e.Status, e.Error)
		}
		return e.Status == status
	})
	return e
}

func (f executionFixture) eventually(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("execution did not reach expected state")
}

func (f executionFixture) assertTargetValue(t *testing.T, want int) {
	t.Helper()
	var value int
	if err := f.target.QueryRow("SELECT value FROM execution_items WHERE id=1").Scan(&value); err != nil || value != want {
		t.Fatalf("target value: got %d, want %d (%v)", value, want, err)
	}
}
