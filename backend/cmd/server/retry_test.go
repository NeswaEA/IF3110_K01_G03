package main

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit/pgfault"
)

// RTY-03: a transient failure on every attempt ends in RETRY_EXHAUSTED after MIGRATION_MAX_ATTEMPTS,
// with one retry_scheduled log per retried attempt and nothing committed.
func TestTransientFailuresExhaustAttempts(t *testing.T) {
	logs := captureLogs(t, "execution.retry_scheduled")
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3})
	f.startWorker(t)
	f.await(t, f.execute(t, f.approved(t, "CREATE TABLE busy_items (id integer)", "DROP TABLE busy_items"), 202).ID, "succeeded")

	holder, err := f.target.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	// every attempt waits past MIGRATION_LOCK_TIMEOUT for this lock (55P03, transient)
	if _, err := holder.ExecContext(t.Context(), "LOCK TABLE busy_items IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	e := f.execute(t, f.approved(t, "INSERT INTO busy_items VALUES (1)", "DELETE FROM busy_items"), 202)
	failed := f.await(t, e.ID, "failed")
	if failed.Error == nil || failed.Error.Code != "RETRY_EXHAUSTED" || failed.Attempt != 3 || failed.HistorySeq != nil {
		t.Fatalf("expected RETRY_EXHAUSTED after 3 attempts: %+v", failed)
	}
	var attempts []int
	for _, entry := range logs.forExecution(e.ID) {
		attempts = append(attempts, entry.Attempt)
	}
	if !slices.Equal(attempts, []int{1, 2}) {
		t.Fatalf("retry_scheduled logs by attempt: %v, want [1 2]", attempts)
	}
	entry := migrationResponse[[]migrations.TimelineEntry](t, f.call("GET", "/history", ""), 200).Data[0]
	var recorded []string
	for _, a := range entry.Attempts {
		if a.SQLExecuted == nil || *a.SQLExecuted != "INSERT INTO busy_items VALUES (1)" || a.Status != "failed" {
			t.Fatalf("retried attempt record: %+v", a)
		}
		recorded = append(recorded, fmt.Sprint(a.Attempt, a.RetryScheduled))
	}
	if entry.Execution.ID != e.ID || !slices.Equal(recorded, []string{"1 true", "2 true", "3 false"}) {
		t.Fatalf("attempt history %v, want three failed attempts with two retries", recorded)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.target.QueryRow("SELECT count(*) FROM busy_items").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("an exhausted execution committed rows: %d (%v)", rows, err)
	}
}

// RTY-04: waiting for the target lock or for an earlier execution never uses up an attempt.
func TestLockAndOrderingWaitsKeepTheAttempt(t *testing.T) {
	// lock_busy redelivers after at least 1s; the default 30s jittered ceiling would outlast the test
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3,
		executor: migrations.ExecutorOptions{LockRetryMaxDelay: time.Second}})
	holder, err := f.target.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(t.Context(), "SELECT pg_advisory_lock("+postgres.MigrationLockKey+")"); err != nil {
		t.Fatal(err)
	}
	first := f.execute(t, f.approved(t, "CREATE TABLE waiting_items (id integer)", "DROP TABLE waiting_items"), 202)
	second := f.execute(t, f.approved(t, "INSERT INTO waiting_items VALUES (1)", "DELETE FROM waiting_items"), 202)
	f.startWorker(t)

	waited := map[string]bool{}
	for deadline := time.After(30 * time.Second); !waited[first.ID+" lock_busy"] || !waited[second.ID+" ordering"]; {
		select {
		case d := <-f.deliveries:
			waited[d.id+" "+d.result.Reason] = !d.result.Ack
		case <-deadline:
			t.Fatalf("expected lock_busy for %s and ordering for %s, saw %v", first.ID, second.ID, waited)
		}
	}
	if _, err := holder.ExecContext(t.Context(), "SELECT pg_advisory_unlock("+postgres.MigrationLockKey+")"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if e := f.await(t, id, "succeeded"); e.Attempt != 1 {
			t.Fatalf("a lock or ordering wait used an attempt: %+v", e)
		}
	}
}

// RTY-05: the target commits but the reply is lost. The next attempt finds the commit
// in the target history and finishes without running the user's SQL again.
func TestUncertainCommitRecoversWithoutReapplying(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3, proxy: true})
	f.startWorker(t)
	f.setupItems(t)
	f.proxy.Arm(pgfault.DropCommitReply, "value=value+1")
	e := f.execute(t, f.approved(t, "UPDATE execution_items SET value=value+1 WHERE id=1", "UPDATE execution_items SET value=value-1 WHERE id=1"), 202)
	f.awaitCommitCut(t)
	f.proxy.Restore()

	done := f.await(t, e.ID, "succeeded")
	if done.Attempt != 2 || done.HistorySeq == nil {
		t.Fatalf("expected the second attempt to record the lost commit: %+v", done)
	}
	f.assertTargetValue(t, 11)
	if n := f.proxy.UserQueries(); n != 1 {
		t.Fatalf("user SQL ran %d times", n)
	}
	entry := migrationResponse[[]migrations.TimelineEntry](t, f.call("GET", "/history", ""), 200).Data[0]
	if entry.Execution.ID != e.ID || len(entry.Attempts) != 2 {
		t.Fatalf("expected the uncertain outcome and its resolution: %+v", entry)
	}
	uncertain, committed := entry.Attempts[0], entry.Attempts[1]
	if uncertain.Attempt != 1 || uncertain.Status != "outcome_unknown" || committed.Attempt != 1 ||
		committed.Status != "succeeded" || committed.HistorySeq == nil || committed.SQLExecuted == nil ||
		*committed.SQLExecuted != "UPDATE execution_items SET value=value+1 WHERE id=1" {
		t.Fatalf("commit must resolve the original attempt: %+v", entry.Attempts)
	}
}

// RTY-06: the target stays unreachable while the worker looks for commit evidence. The execution
// ends outcome_unknown, blocks the database, and a manual reconcile settles it from the evidence.
func TestUnreachableEvidenceBlocksUntilReconciled(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3, proxy: true,
		executor: migrations.ExecutorOptions{StaleAfter: 200 * time.Millisecond}})
	f.startWorker(t)
	f.setupItems(t)
	f.proxy.Arm(pgfault.DropCommitReply, "value=value+1")
	e := f.execute(t, f.approved(t, "UPDATE execution_items SET value=value+1 WHERE id=1", "UPDATE execution_items SET value=value-1 WHERE id=1"), 202)
	f.awaitCommitCut(t)

	unknown := f.await(t, e.ID, "outcome_unknown")
	if unknown.Attempt != 3 || unknown.HistorySeq != nil {
		t.Fatalf("expected outcome_unknown after the last evidence attempt: %+v", unknown)
	}
	head := migrationResponse[migrations.DatabaseHead](t, f.call("GET", "/head", ""), 200).Data
	if !head.Blocked || head.BlockingExecutionID == nil || *head.BlockingExecutionID != e.ID {
		t.Fatalf("the unknown outcome did not block the database: %+v", head)
	}
	next := f.execute(t, f.approved(t, "UPDATE execution_items SET value=value*2 WHERE id=1", "UPDATE execution_items SET value=value/2 WHERE id=1"), 202)
	if next.Status != "held" {
		t.Fatalf("expected a held execution behind the unknown outcome: %+v", next)
	}

	f.proxy.Restore()
	if r := migrationResponse[migrations.Execution](t, f.call("POST", "/executions/"+e.ID+"/reconcile", `{}`), 202).Data; r.Status != "queued" {
		t.Fatalf("unexpected reconcile job: %+v", r)
	}
	if settled := f.await(t, e.ID, "succeeded"); settled.HistorySeq == nil {
		t.Fatalf("reconcile did not record the commit: %+v", settled)
	}
	f.await(t, next.ID, "succeeded")
	f.assertTargetValue(t, 22) // (10+1)*2: each migration applied once
	if n := f.proxy.UserQueries(); n != 1 {
		t.Fatalf("user SQL of the uncertain execution ran %d times", n)
	}
}

// LCK-01: a second worker that receives the same job while the first runs it gets lock_busy,
// and the migration runs once.
func TestSecondWorkerWaitsForTheTargetLock(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{})
	e := f.execute(t, f.approved(t, "CREATE TABLE slow_items (id integer); SELECT pg_sleep(1)", "DROP TABLE slow_items"), 202)
	job := contracts.JobMessage{ExecutionID: e.ID, DatabaseID: f.targetID}
	first := make(chan contracts.JobResult, 1)
	go func() { first <- f.worker.Handle(t.Context(), job) }()
	f.eventually(t, func() bool {
		return migrationResponse[migrations.Execution](t, f.call("GET", "/executions/"+e.ID, ""), 200).Data.Status == "running"
	})

	other := f.newWorker("other-worker")
	if busy := other.Handle(t.Context(), job); busy.Ack || busy.Reason != "lock_busy" {
		t.Fatalf("second worker should wait for the lock: %+v", busy)
	}
	if done := <-first; !done.Ack || done.Reason != "done" {
		t.Fatalf("first worker: %+v", done)
	}
	if again := other.Handle(t.Context(), job); !again.Ack || again.Reason != "duplicate" {
		t.Fatalf("redelivery after completion: %+v", again)
	}
	if e := f.await(t, e.ID, "succeeded"); e.Attempt != 1 {
		t.Fatalf("the waiting worker used an attempt: %+v", e)
	}
	var runs int
	if err := f.target.QueryRow("SELECT count(*) FROM _platform.migration_history WHERE execution_id=$1", e.ID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("history rows for the execution: %d (%v)", runs, err)
	}
}

func (f executionFixture) setupItems(t *testing.T) {
	t.Helper()
	setup := f.approved(t, "CREATE TABLE execution_items (id integer PRIMARY KEY, value integer NOT NULL); INSERT INTO execution_items VALUES (1, 10)",
		"DROP TABLE execution_items")
	f.await(t, f.execute(t, setup, 202).ID, "succeeded")
}

// awaitCommitCut waits until the proxy dropped the reply of a commit the server confirmed.
func (f executionFixture) awaitCommitCut(t *testing.T) {
	t.Helper()
	select {
	case event := <-f.proxy.Events():
		if event.Mode != pgfault.DropCommitReply || !event.CommitConfirmed {
			t.Fatalf("unexpected proxy event: %+v", event)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the commit never reached the proxy")
	}
}
