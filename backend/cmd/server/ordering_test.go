package main

import (
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
)

func TestExecutionWakesNextJob(t *testing.T) {
	const fallback = 10 * time.Second
	f := newExecutionFixtureWithOrdering(t, database.SQLite, fallback)
	requests := []migrations.ChangeRequest{
		f.approved(t, "CREATE TABLE ordered (id integer)", "DROP TABLE ordered"),
		f.approved(t, "INSERT INTO ordered VALUES (1)", "DELETE FROM ordered WHERE id=1"),
		f.approved(t, "INSERT INTO ordered VALUES (2)", "DELETE FROM ordered WHERE id=2"),
	}
	first, second, third := f.execute(t, requests[0], 202), f.execute(t, requests[1], 202), f.execute(t, requests[2], 202)

	// Deliver only the first message initially, simulating successors whose
	// original messages are unavailable until the ordering fallback expires.
	f.exec(t, `UPDATE outbox_messages SET published_at=now() WHERE payload->>'execution_id' IN ($1,$2)`, second.ID, third.ID)
	start := time.Now()
	f.startWorker(t)
	res := f.await(t, third.ID, "succeeded")
	if res.Version == nil || *res.Version != 3 || time.Since(start) >= fallback {
		t.Fatalf("third execution did not run in order before the fallback: %+v", res)
	}
	f.assertOutboxCount(t, first.ID, 1)
	f.assertOutboxCount(t, second.ID, 2)
	f.assertOutboxCount(t, third.ID, 2)
	for len(f.deliveries) > 0 {
		d := <-f.deliveries
		if d.result.Reason == "ordering" {
			t.Fatalf("ordered job fell back to redelivery: %+v", d)
		}
	}
}

func TestCancelExecutionWakesNextJob(t *testing.T) {
	f := newExecutionFixture(t, database.SQLite)
	req := f.approved(t, "CREATE TABLE ordered (id integer)", "DROP TABLE ordered")
	first := f.execute(t, req, 202)
	second := f.execute(t, f.approved(t, "CREATE TABLE next (id integer)", "DROP TABLE next"), 202)
	third := f.execute(t, f.approved(t, "CREATE TABLE later (id integer)", "DROP TABLE later"), 202)
	path := "/executions/" + first.ID + "/cancel"

	f.exec(t, `CREATE FUNCTION test_reject_wake() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test wake failure'; END $$`)
	f.exec(t, `CREATE TRIGGER test_reject_wake BEFORE INSERT ON outbox_messages
		FOR EACH ROW EXECUTE FUNCTION test_reject_wake()`)
	migrationError(t, f.call("POST", path, `{}`), 500, "INTERNAL_ERROR")
	f.assertExecutionStatus(t, first.ID, "queued")
	requestPath := "/change-requests/" + req.ID
	stillQueued := migrationResponse[migrations.ChangeRequest](t, f.call("GET", requestPath, ""), 200).Data
	if stillQueued.Status != "executed" {
		t.Fatalf("failed wake must roll back the change request update: %+v", stillQueued)
	}
	f.assertOutboxCount(t, second.ID, 1)
	f.exec(t, "DROP TRIGGER test_reject_wake ON outbox_messages")

	e := migrationResponse[migrations.Execution](t, f.call("POST", path, `{}`), 200).Data
	if e.Status != "cancelled" || e.FinishedAt == nil {
		t.Fatalf("unexpected cancellation: %+v", e)
	}
	f.assertOutboxCount(t, second.ID, 2)
	f.assertOutboxCount(t, third.ID, 1)
	migrationError(t, f.call("POST", path, `{}`), 409, "EXECUTION_NOT_ACTIVE")
	f.assertOutboxCount(t, second.ID, 2)

	r := migrationResponse[migrations.ChangeRequest](t, f.call("GET", requestPath, ""), 200).Data
	if r.Status != "approved" || r.ExecutionID != nil || r.ID != req.ID {
		t.Fatalf("cancellation did not reopen request: %+v", r)
	}
	requeued := f.execute(t, r, 202)
	if requeued.ID == e.ID || requeued.Number != 4 {
		t.Fatalf("expected a new execution for the same request: %+v", requeued)
	}
	executed := migrationResponse[migrations.ChangeRequest](t, f.call("GET", requestPath, ""), 200).Data
	if executed.ExecutionID == nil || *executed.ExecutionID != requeued.ID {
		t.Fatalf("request must point to the new execution: %+v", executed)
	}
	f.assertExecutionStatus(t, first.ID, "cancelled")
}

func TestExecutionDoesNotWakeOnRetryOrBlock(t *testing.T) {
	t.Run("transient retry", func(t *testing.T) {
		f := newExecutionFixture(t, database.Postgres)
		// A real table lock gives the worker SQLSTATE 55P03.
		if _, err := f.target.Exec("CREATE TABLE retry_locked (id integer)"); err != nil {
			t.Fatal(err)
		}
		first := f.execute(t, f.approved(t, "SELECT * FROM retry_locked", "SELECT 1"), 202)
		second := f.execute(t, f.approved(t, "SELECT 2", "SELECT 2"), 202)
		f.exec(t, "UPDATE migration_executions SET max_attempts=3 WHERE id=$1", first.ID)
		tx, err := f.target.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec("LOCK TABLE retry_locked IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		result := f.worker.Handle(t.Context(), contracts.JobMessage{ExecutionID: first.ID, DatabaseID: f.targetID})
		if result.Ack || result.Reason != "retry" {
			t.Fatalf("expected retry after table lock: %+v", result)
		}
		e := f.assertExecutionStatus(t, first.ID, "queued")
		if e.Attempt != 1 || e.Error == nil || e.Error.Code != "LOCK_TIMEOUT" {
			t.Fatalf("unexpected retry state: %+v", e)
		}
		f.assertOutboxCount(t, first.ID, 1)
		f.assertOutboxCount(t, second.ID, 1)
	})

	t.Run("outcome unknown", func(t *testing.T) {
		f := newExecutionFixture(t, database.SQLite)
		first := f.execute(t, f.approved(t, "SELECT 1", "SELECT 1"), 202)
		second := f.execute(t, f.approved(t, "SELECT 2", "SELECT 2"), 202)
		f.exec(t, `UPDATE migration_executions SET reconcile_requested=true,attempt=1 WHERE id=$1`, first.ID)
		// An uninitialized target makes evidence unavailable.
		if _, err := f.target.Exec("DROP TABLE _platform_migration_history"); err != nil {
			t.Fatal(err)
		}
		migrationError(t, f.call("POST", "/executions/"+first.ID+"/cancel", `{}`), 409, "EXECUTION_NOT_ACTIVE")
		result := f.worker.Handle(t.Context(), contracts.JobMessage{ExecutionID: first.ID, DatabaseID: f.targetID})
		if !result.Ack {
			t.Fatalf("unverifiable final attempt should stop for reconcile: %+v", result)
		}
		f.assertExecutionStatus(t, first.ID, "outcome_unknown")
		result = f.worker.Handle(t.Context(), contracts.JobMessage{ExecutionID: second.ID, DatabaseID: f.targetID})
		if !result.Ack || result.Reason != "held" {
			t.Fatalf("next execution should be held: %+v", result)
		}
		f.assertOutboxCount(t, second.ID, 1)
		f.assertExecutionStatus(t, second.ID, "held")
		e := migrationResponse[migrations.Execution](t, f.call("POST", "/executions/"+second.ID+"/cancel", `{}`), 200).Data
		if e.Status != "cancelled" {
			t.Fatalf("held execution was not cancelled: %+v", e)
		}
	})
}

func (f executionFixture) assertOutboxCount(t *testing.T, executionID string, want int) {
	t.Helper()
	var got int
	if err := f.meta.QueryRow(`SELECT count(*) FROM outbox_messages WHERE payload->>'execution_id'=$1`, executionID).Scan(&got); err != nil || got != want {
		t.Fatalf("outbox messages for %s: got %d, want %d (%v)", executionID, got, want, err)
	}
}

func (f executionFixture) assertExecutionStatus(t *testing.T, id, status string) migrations.Execution {
	t.Helper()
	e := migrationResponse[migrations.Execution](t, f.call("GET", "/executions/"+id, ""), 200).Data
	if e.Status != status {
		t.Fatalf("execution %s: got %s, want %s", id, e.Status, status)
	}
	return e
}
