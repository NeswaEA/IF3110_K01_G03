package main

import (
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
)

func TestInterruptedAttemptAppearsInHistory(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3})
	e := f.execute(t, f.approved(t, "SELECT 1; SELECT 2", "SELECT 1"), 202)
	// Metadata left behind by a worker that died after sending its first statement.
	f.exec(t, `UPDATE migration_executions SET status='running', attempt=1, started_at=now(), heartbeat_at=now(),
		worker_id='interrupted-worker', attempt_progress_sql='SELECT 1' WHERE id=$1`, e.ID)
	f.startWorker(t)
	f.await(t, e.ID, "succeeded")
	entry := migrationResponse[[]migrations.TimelineEntry](t, f.call("GET", "/history", ""), 200).Data[0]
	if entry.Execution.ID != e.ID || len(entry.Attempts) != 2 {
		t.Fatalf("expected both attempts in the timeline: %+v", entry)
	}
	first, second := entry.Attempts[0], entry.Attempts[1]
	if first.Attempt != 1 || first.Status != "outcome_unknown" || first.SQLExecuted == nil || *first.SQLExecuted != "SELECT 1" ||
		second.Attempt != 2 || second.Status != "succeeded" || second.SQLExecuted == nil || !strings.Contains(*second.SQLExecuted, "SELECT 2") {
		t.Fatalf("attempt outcomes or SQL were lost: %+v", entry.Attempts)
	}
}

func TestHistoryRecordsOnlySentStatements(t *testing.T) {
	f := newExecutionFixture(t, database.Postgres)
	f.startWorker(t)
	f.setupItems(t)
	e := f.execute(t, f.approved(t,
		"UPDATE execution_items SET value=value+1 WHERE id=1; SELECT 1/0; SELECT 424242", "SELECT 1"), 202)
	f.await(t, e.ID, "failed")
	f.assertTargetValue(t, 10)
	entry := migrationResponse[[]migrations.TimelineEntry](t, f.call("GET", "/history", ""), 200).Data[0]
	if entry.Execution.ID != e.ID || len(entry.Attempts) != 1 || entry.Attempts[0].SQLExecuted == nil {
		t.Fatalf("unexpected attempt record: %+v", entry)
	}
	if sql := *entry.Attempts[0].SQLExecuted; !strings.Contains(sql, "UPDATE execution_items") ||
		!strings.Contains(sql, "SELECT 1/0") || strings.Contains(sql, "424242") {
		t.Fatalf("history must contain only the statements sent, including the failing statement: %s", sql)
	}
}

func TestHideRollbackKeepsFailedAttempts(t *testing.T) {
	f := newExecutionFixtureWithOptions(t, database.Postgres, executionFixtureOptions{maxAttempts: 3})
	f.startWorker(t)
	f.setupItems(t)
	holder, err := f.target.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback()
	if _, err := holder.ExecContext(t.Context(), "LOCK TABLE execution_items IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	run := f.execute(t, f.approved(t, "UPDATE execution_items SET value=value+1", "UPDATE execution_items SET value=value-1"), 202)
	f.eventually(t, func() bool {
		var recorded bool
		if err := f.meta.QueryRow("SELECT EXISTS (SELECT 1 FROM migration_attempts WHERE execution_id=$1 AND status='failed')", run.ID).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		return recorded
	})
	if err := holder.Rollback(); err != nil {
		t.Fatal(err)
	}
	f.await(t, run.ID, "succeeded")
	rollback := f.execute(t, f.request(t, run.MigrationID, "rollback"), 202)
	f.await(t, rollback.ID, "succeeded")
	entries := migrationResponse[[]migrations.TimelineEntry](t, f.call("GET", "/history?hide_rollback=true", ""), 200).Data
	var kept bool
	for _, entry := range entries {
		switch entry.Execution.ID {
		case rollback.ID:
			t.Fatal("successful rollback remained visible")
		case run.ID:
			kept = true
			if !entry.AttemptsOnly || entry.Committed != nil || len(entry.Attempts) != 1 || entry.Attempts[0].Status != "failed" {
				t.Fatalf("rolled-back execution must retain only its failed attempt: %+v", entry)
			}
		}
	}
	if !kept {
		t.Fatal("the failed attempt disappeared after rollback")
	}
}
