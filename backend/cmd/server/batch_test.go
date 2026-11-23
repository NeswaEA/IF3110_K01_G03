package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
)

func TestDryRunLeavesTargetUnchanged(t *testing.T) {
	for _, engine := range []database.Driver{database.Postgres, database.SQLite} {
		t.Run(string(engine), func(t *testing.T) {
			f := newExecutionFixture(t, engine)
			m := migrationResponse[migrations.Migration](t, f.call("POST", "/migrations",
				`{"title":"Dry run","mode":"normal","up_sql":"CREATE TABLE preview(id integer)","down_sql":"DROP TABLE preview"}`), 201).Data
			path := "/migrations/" + m.ID + "/dry-run"
			e := migrationResponse[migrations.Execution](t, f.call("POST", path, `{}`), 202).Data
			if replay := migrationResponse[migrations.Execution](t, f.call("POST", path, `{}`), 200).Data; replay.ID != e.ID {
				t.Fatal("duplicate dry run")
			}
			f.startWorker(t)
			r := f.await(t, e.ID, "succeeded")
			var report migrations.DryRunReport
			if err := json.Unmarshal(r.DryRun, &report); err != nil || report.Status != "passed" || report.Reversible == nil || !*report.Reversible || len(report.SchemaChanges) != 1 {
				t.Fatalf("dry run report: %+v, %v", report, err)
			}
			m = migrationResponse[migrations.Migration](t, f.call("GET", "/migrations/"+m.ID, ""), 200).Data
			if m.Locked || string(m.LatestDryRun) != string(r.DryRun) {
				t.Fatalf("dry run locked migration or did not persist report: %+v", m)
			}
			if _, err := f.target.Exec("SELECT * FROM preview"); err == nil {
				t.Fatal("dry run committed SQL")
			}
		})
	}
}

func TestDryRunReportsUnreversedSchemaErrorAndTimeout(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{
		executor: migrations.ExecutorOptions{DryRunTimeout: 400 * time.Millisecond},
	})
	f.startWorker(t)
	for _, tc := range []struct {
		name, up, wantExecution, wantReport string
	}{
		{"unreversed", "CREATE TABLE preview(id integer)", "succeeded", "passed"},
		{"error", "CREATE TABLE preview(id integer); SELECT * FROM undefined_table", "failed", "failed"},
		{"timeout", "CREATE TABLE preview(id integer); SELECT pg_sleep(2)", "failed", "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"title": tc.name, "up_sql": tc.up, "down_sql": "SELECT 1", "mode": "normal"})
			if err != nil {
				t.Fatal(err)
			}
			m := migrationResponse[migrations.Migration](t, f.call("POST", "/migrations", string(body)), 201).Data
			e := migrationResponse[migrations.Execution](t, f.call("POST", "/migrations/"+m.ID+"/dry-run", "{}"), 202).Data
			result := f.await(t, e.ID, tc.wantExecution)
			var report migrations.DryRunReport
			if err := json.Unmarshal(result.DryRun, &report); err != nil || report.Status != tc.wantReport {
				t.Fatalf("dry-run report: %+v (%v)", report, err)
			}
			if tc.name == "unreversed" && (report.Reversible == nil || *report.Reversible) {
				t.Fatal("the report should mark the down SQL as insufficient")
			}
			if tc.name == "error" && (report.Message == nil || !strings.Contains(*report.Message, "undefined_table")) {
				t.Fatalf("missing PostgreSQL diagnostic: %+v", report)
			}
			if _, err := f.target.Exec("SELECT * FROM preview"); err == nil {
				t.Fatal("dry-run left a table on the target")
			}
		})
	}
	var count int
	if err := f.target.QueryRow("SELECT count(*) FROM _platform.migration_history").Scan(&count); err != nil || count != 0 {
		t.Fatalf("dry-run wrote target history: %d, %v", count, err)
	}
}

func TestBatchStopResumeRollback(t *testing.T) {
	f := newExecutionFixture(t, database.Postgres)
	prepare := f.approved(t, "CREATE TABLE batch_items(id bigint PRIMARY KEY, price integer); INSERT INTO batch_items SELECT id, 0 FROM generate_series(1,8) id", "DROP TABLE batch_items")
	f.startWorker(t)
	f.await(t, f.execute(t, prepare, 202).ID, "succeeded")
	m := f.batchMigration(t, 2000, "price + 1", "price - 1")
	dry := migrationResponse[migrations.Execution](t, f.call("POST", "/migrations/"+m.ID+"/dry-run", `{}`), 202).Data
	f.await(t, dry.ID, "succeeded")
	f.assertBatchPrices(t, 0)
	first := f.execute(t, f.request(t, m.ID, "apply"), 202)
	f.eventually(t, func() bool {
		e := migrationResponse[migrations.Execution](t, f.call("GET", "/executions/"+first.ID, ""), 200).Data
		return e.Batch != nil && e.Batch.BatchesCommitted >= 1
	})
	if _, err := f.target.Exec("INSERT INTO batch_items VALUES (9, 0)"); err != nil {
		t.Fatal(err)
	}
	migrationResponse[migrations.Execution](t, f.call("POST", "/executions/"+first.ID+"/stop", `{}`), 202)
	partial := f.await(t, first.ID, "partial")
	if partial.Batch.RowsCommitted != "2" {
		t.Fatalf("expected one checkpoint before stop: %+v", partial.Batch)
	}
	// A later job remains held until resume unblocks the database.
	held := f.execute(t, f.approved(t, "SELECT 1", "SELECT 1"), 202)
	if held.Status != "held" {
		t.Fatalf("expected held after partial: %+v", held)
	}
	resumed := migrationResponse[migrations.Execution](t, f.call("POST", "/executions/"+first.ID+"/resume", `{"batch_size":8}`), 202).Data
	f.await(t, resumed.ID, "succeeded")
	f.await(t, held.ID, "succeeded")
	var latePrice int
	if err := f.target.QueryRow("DELETE FROM batch_items WHERE id=9 RETURNING price").Scan(&latePrice); err != nil || latePrice != 0 {
		t.Fatalf("row added beyond the original scope was changed: price %d (%v)", latePrice, err)
	}
	f.assertBatchPrices(t, 1)
	// Roll back the head produced by the held job, then the batch chain.
	f.await(t, f.execute(t, f.request(t, held.MigrationID, "rollback"), 202).ID, "succeeded")
	rolled := f.await(t, f.execute(t, f.request(t, m.ID, "rollback"), 202).ID, "succeeded")
	if rolled.RevertsExecution == nil || *rolled.RevertsExecution != resumed.ID {
		t.Fatalf("rollback should point to committed resume: %+v", rolled)
	}
	f.assertBatchPrices(t, 0)
}

func TestBatchRollbackVerifiesReturnedKeys(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "mismatched keys"
		wantError := "INVALID_BATCH_RESULT"
		if legacy {
			name = "legacy checkpoint"
			wantError = "LEDGER_INCOMPATIBLE"
		}
		t.Run(name, func(t *testing.T) {
			f := newExecutionFixture(t, database.Postgres)
			f.startWorker(t)
			setup := f.approved(t, "CREATE TABLE batch_items(id bigint PRIMARY KEY,price integer); INSERT INTO batch_items SELECT id,0 FROM generate_series(1,3) id", "DROP TABLE batch_items")
			f.await(t, f.execute(t, setup, 202).ID, "succeeded")
			template := func(filter, expr string) string {
				return "WITH b AS (SELECT id FROM batch_items WHERE id > $1 AND id <= $2 AND " + filter +
					" ORDER BY id LIMIT $3) UPDATE batch_items SET price=" + expr + " FROM b WHERE batch_items.id=b.id RETURNING batch_items.id"
			}
			downFilter := "id<>1"
			if legacy {
				downFilter = "id<>2"
			}
			body, err := json.Marshal(map[string]any{
				"title": name, "mode": "batch", "up_sql": template("id<>2", "price+1"), "down_sql": template(downFilter, "price-1"),
				"batch": map[string]any{"schema": "public", "table": "batch_items", "key_column": "id", "batch_size": 2, "pause_ms": 0},
			})
			if err != nil {
				t.Fatal(err)
			}
			m := migrationResponse[migrations.Migration](t, f.call("POST", "/migrations", string(body)), 201).Data
			dry := migrationResponse[migrations.Execution](t, f.call("POST", "/migrations/"+m.ID+"/dry-run", `{}`), 202).Data
			var report migrations.DryRunReport
			if err := json.Unmarshal(f.await(t, dry.ID, "succeeded").DryRun, &report); err != nil || report.Reversible == nil || *report.Reversible != legacy {
				t.Fatalf("dry run must compare returned keys: %+v (%v)", report, err)
			}
			f.await(t, f.execute(t, f.request(t, m.ID, "apply"), 202).ID, "succeeded")
			if legacy {
				if _, err := f.target.Exec("ALTER TABLE _platform.migration_checkpoints DROP COLUMN key_fingerprint"); err != nil {
					t.Fatal(err)
				}
			}
			rollback := f.execute(t, f.request(t, m.ID, "rollback"), 202)
			outcome := f.await(t, rollback.ID, "failed")
			if outcome.Error == nil || outcome.Error.Code != wantError {
				t.Fatalf("expected %s, got %+v", wantError, outcome.Error)
			}
			var prices string
			if err := f.target.QueryRow("SELECT string_agg(price::text, ',' ORDER BY id) FROM batch_items").Scan(&prices); err != nil || prices != "1,0,1" {
				t.Fatalf("rejected rollback changed target rows: %s (%v)", prices, err)
			}
		})
	}
}

func TestBatchRevertsCommittedRangesAfterFailure(t *testing.T) {
	logs := captureBatchLogs(t)
	f := newExecutionFixture(t, database.Postgres)
	setup := f.approved(t, "CREATE TABLE batch_items(id bigint PRIMARY KEY, price integer CHECK(price>=0)); INSERT INTO batch_items SELECT id,0 FROM generate_series(1,8) id", "DROP TABLE batch_items")
	f.startWorker(t)
	f.await(t, f.execute(t, setup, 202).ID, "succeeded")
	m := f.batchMigration(t, 0, "CASE WHEN batch_items.id=3 THEN -1 ELSE price+1 END", "price - 1")
	first := f.execute(t, f.request(t, m.ID, "apply"), 202)
	f.await(t, first.ID, "partial")
	outcomes := logs.forExecution(first.ID)
	if len(outcomes) != 2 ||
		outcomes[0] != (batchLog{ExecutionID: first.ID, Event: "batch.committed", Status: "committed", Direction: "up", Batch: 1, Rows: 2, Attempt: 1}) ||
		outcomes[1] != (batchLog{ExecutionID: first.ID, Event: "batch.failed", Status: "failed", Direction: "up", Batch: 2, ErrorCode: "SQL_EXECUTION_FAILED", Attempt: 1}) {
		t.Fatalf("expected one committed batch followed by a failed batch: %+v", outcomes)
	}
	f.assertBatchPrices(t, -1)
	req := migrationResponse[migrations.ChangeRequest](t, f.call("POST", "/migrations/"+m.ID+"/requests", `{"action":"rollback","target_execution_id":"`+first.ID+`"}`), 201).Data
	req = migrationResponse[migrations.ChangeRequest](t, f.call("POST", "/change-requests/"+req.ID+"/approve", `{}`), 200).Data
	f.await(t, f.execute(t, req, 202).ID, "succeeded")
	f.assertBatchPrices(t, 0)
	m = migrationResponse[migrations.Migration](t, f.call("GET", "/migrations/"+m.ID, ""), 200).Data
	if m.Status != "pending" || !m.Locked {
		t.Fatalf("partial apply recovery must reset position and preserve SQL lock: %+v", m)
	}
}

func TestBatchResumesFailedRollback(t *testing.T) {
	f := newExecutionFixture(t, database.Postgres)
	setup := f.approved(t, `CREATE TABLE batch_items(id bigint PRIMARY KEY, price integer CHECK(price>=0));
		INSERT INTO batch_items SELECT id,0 FROM generate_series(1,8) id;
		CREATE TABLE batch_switch(fail boolean);
		INSERT INTO batch_switch VALUES (true)`, "DROP TABLE batch_items, batch_switch")
	f.startWorker(t)
	f.await(t, f.execute(t, setup, 202).ID, "succeeded")
	m := f.batchMigration(t, 0, "price+1",
		"CASE WHEN batch_items.id=3 AND (SELECT fail FROM batch_switch) THEN -1 ELSE price-1 END")
	f.await(t, f.execute(t, f.request(t, m.ID, "apply"), 202).ID, "succeeded")
	rollback := f.execute(t, f.request(t, m.ID, "rollback"), 202)
	partial := f.await(t, rollback.ID, "partially_reverted")
	if partial.Batch.RowsCommitted != "4" || *partial.Batch.Cursor != "4" {
		t.Fatalf("rollback must process the highest checkpoint ranges first: %+v", partial.Batch)
	}
	if _, err := f.target.Exec("UPDATE batch_switch SET fail=false"); err != nil {
		t.Fatal(err)
	}
	resumed := migrationResponse[migrations.Execution](t, f.call("POST", "/executions/"+rollback.ID+"/resume", "{}"), 202).Data
	f.await(t, resumed.ID, "succeeded")
	f.assertBatchPrices(t, 0)
	m = migrationResponse[migrations.Migration](t, f.call("GET", "/migrations/"+m.ID, ""), 200).Data
	if m.Status != "rolled_back" {
		t.Fatalf("resumed rollback did not update the migration: %+v", m)
	}
}

func TestBatchStatementTimeoutCanResumeWithSmallerBatches(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{
		executor: migrations.ExecutorOptions{BatchTimeout: 400 * time.Millisecond},
	})
	setup := f.approved(t, "CREATE TABLE batch_items(id bigint PRIMARY KEY, price integer); INSERT INTO batch_items SELECT id,0 FROM generate_series(1,8) id", "DROP TABLE batch_items")
	f.startWorker(t)
	f.await(t, f.execute(t, setup, 202).ID, "succeeded")
	m := f.batchMigration(t, 0, "price + (SELECT 1 FROM pg_sleep(CASE WHEN batch_items.id=3 AND (SELECT count(*) FROM b)>1 THEN 1 ELSE 0 END))", "price - 1")
	run := f.execute(t, f.request(t, m.ID, "apply"), 202)
	partial := f.await(t, run.ID, "partial")
	if partial.Error == nil || partial.Error.Code != "STATEMENT_TIMEOUT" || partial.Batch.RowsCommitted != "2" {
		t.Fatalf("expected a timeout after the first batch: %+v", partial)
	}
	resumed := migrationResponse[migrations.Execution](t, f.call("POST", "/executions/"+run.ID+"/resume", `{"batch_size":1}`), 202).Data
	f.await(t, resumed.ID, "succeeded")
	f.assertBatchPrices(t, 1)
}

func TestBatchRecoversCheckpointAfterMirrorFailure(t *testing.T) {
	logs := captureBatchLogs(t)
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3})
	setup := f.approved(t, "CREATE TABLE batch_items(id bigint PRIMARY KEY, price integer); INSERT INTO batch_items SELECT id,0 FROM generate_series(1,8) id", "DROP TABLE batch_items")
	f.startWorker(t)
	f.await(t, f.execute(t, setup, 202).ID, "succeeded")
	// The first mirror INSERT fails after the target committed its checkpoint.
	// A sequence survives the metadata rollback so the redelivery can recover.
	f.exec(t, `CREATE SEQUENCE batch_mirror_attempt;
		CREATE FUNCTION reject_first_batch_mirror() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval('batch_mirror_attempt') = 1 THEN
				RAISE EXCEPTION 'injected mirror failure';
			END IF;
			RETURN NEW;
		END; $$;
		CREATE TRIGGER reject_first_batch_mirror BEFORE INSERT ON migration_batches
			FOR EACH ROW EXECUTE FUNCTION reject_first_batch_mirror()`)
	m := f.batchMigration(t, 0, "price + 1", "price - 1")
	e := f.await(t, f.execute(t, f.request(t, m.ID, "apply"), 202).ID, "succeeded")
	f.assertBatchPrices(t, 1)
	var target, mirror int
	if err := f.target.QueryRow("SELECT count(*) FROM _platform.migration_checkpoints WHERE execution_id=$1", e.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err := f.meta.QueryRow("SELECT count(*) FROM migration_batches WHERE execution_id=$1", e.ID).Scan(&mirror); err != nil {
		t.Fatal(err)
	}
	if e.Attempt < 2 || target != 4 || mirror != target || e.Batch.RowsCommitted != "8" {
		t.Fatalf("checkpoint recovery repeated work or missed evidence: attempt=%d target=%d mirror=%d batch=%+v", e.Attempt, target, mirror, e.Batch)
	}
	outcomes := logs.forExecution(e.ID)
	if len(outcomes) != mirror {
		t.Fatalf("expected one log per mirrored checkpoint: %+v", outcomes)
	}
	for i, outcome := range outcomes {
		if outcome.Event != "batch.committed" || outcome.Batch != i+1 || outcome.Rows != 2 {
			t.Fatalf("unexpected recovered batch outcome: %+v", outcome)
		}
	}
}

type batchLog struct {
	ExecutionID string `json:"execution_id"`
	Event       string `json:"event"`
	Status      string `json:"status"`
	Direction   string `json:"direction"`
	Batch       int    `json:"batch"`
	Rows        int    `json:"rows"`
	ErrorCode   string `json:"error_code"`
	Attempt     int    `json:"attempt"`
}

type batchLogRecorder struct {
	mu      sync.Mutex
	events  []string
	entries []batchLog
}

func captureBatchLogs(t *testing.T) *batchLogRecorder {
	t.Helper()
	return captureLogs(t, "batch.committed", "batch.failed")
}

// captureLogs records the JSON log lines of the given events until the test ends.
func captureLogs(t *testing.T, events ...string) *batchLogRecorder {
	t.Helper()
	previous, output, flags := slog.Default(), log.Writer(), log.Flags()
	recorder := &batchLogRecorder{events: events}
	slog.SetDefault(slog.New(slog.NewJSONHandler(recorder, nil)))
	t.Cleanup(func() {
		// SetDefault redirected the log package to the recorder; restoring slog alone leaves it there.
		slog.SetDefault(previous)
		log.SetOutput(output)
		log.SetFlags(flags)
	})
	return recorder
}

func (r *batchLogRecorder) Write(line []byte) (int, error) {
	var entry batchLog
	if json.Unmarshal(line, &entry) == nil && slices.Contains(r.events, entry.Event) {
		r.mu.Lock()
		r.entries = append(r.entries, entry)
		r.mu.Unlock()
	}
	return len(line), nil
}

func (r *batchLogRecorder) forExecution(id string) []batchLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	var entries []batchLog
	for _, entry := range r.entries {
		if entry.ExecutionID == id {
			entries = append(entries, entry)
		}
	}
	return entries
}

func TestBatchWithCommittedChangesCannotBeCancelledDuringRetry(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{
		maxAttempts: 3,
		executor:    migrations.ExecutorOptions{RetryBaseDelay: 2 * time.Second, RetryMaxDelay: 2 * time.Second},
	})
	setup := f.approved(t, "CREATE TABLE batch_items(id bigint PRIMARY KEY, price integer); INSERT INTO batch_items SELECT id,0 FROM generate_series(1,8) id", "DROP TABLE batch_items")
	f.startWorker(t)
	f.await(t, f.execute(t, setup, 202).ID, "succeeded")
	lock, err := f.target.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(t.Context(), "SELECT id FROM batch_items WHERE id=3 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	m := f.batchMigration(t, 0, "price + 1", "price - 1")
	req := f.request(t, m.ID, "apply")
	e := f.execute(t, req, 202)
	f.eventually(t, func() bool {
		value := migrationResponse[migrations.Execution](t, f.call("GET", "/executions/"+e.ID, ""), 200).Data
		return value.Status == "queued" && value.Batch.BatchesCommitted == 1
	})
	res := f.call("POST", "/executions/"+e.ID+"/cancel", "{}")
	if res.Code != 409 || !strings.Contains(res.Body.String(), "EXECUTION_NOT_ACTIVE") {
		t.Fatalf("cancel should preserve the batch chain: %d %s", res.Code, res.Body.String())
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	f.await(t, e.ID, "succeeded")
	f.assertBatchPrices(t, 1)
}

func (f executionFixture) batchMigration(t *testing.T, pause int, upExpression, downExpression string) migrations.Migration {
	t.Helper()
	const prefix = "WITH b AS (SELECT id FROM batch_items WHERE id>$1 AND id<=$2 ORDER BY id LIMIT $3) UPDATE batch_items SET price="
	const suffix = " FROM b WHERE batch_items.id=b.id RETURNING batch_items.id"
	body, err := json.Marshal(map[string]any{"title": "Batch items", "mode": "batch", "up_sql": prefix + upExpression + suffix,
		"down_sql": prefix + downExpression + suffix, "batch": map[string]any{"schema": "public", "table": "batch_items", "key_column": "id", "batch_size": 2, "pause_ms": pause}})
	if err != nil {
		t.Fatal(err)
	}
	return migrationResponse[migrations.Migration](t, f.call("POST", "/migrations", string(body)), 201).Data
}

func (f executionFixture) assertBatchPrices(t *testing.T, want int) {
	t.Helper()
	var bad int
	query := "SELECT count(*) FROM batch_items WHERE price<>$1"
	if want == -1 {
		query = "SELECT count(*) FROM batch_items WHERE price<>CASE WHEN id<=2 THEN 1 ELSE 0 END AND $1=-1"
	}
	if err := f.target.QueryRow(query, want).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("unexpected batch values: %d rows (%v)", bad, err)
	}
}
