package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func postgresRegistryService(t *testing.T) (*Service, database.TargetHandle) {
	t.Helper()
	u, err := url.Parse(requireEnv(t, "TEST_TARGET_DATABASE_URL"))
	if err != nil || u.User == nil {
		t.Fatal("invalid target DSN")
	}
	port := 5432
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			t.Fatal("invalid target port")
		}
	}
	password, _ := u.User.Password()
	sslmode := u.Query().Get("sslmode")
	if sslmode == "" {
		sslmode = "require"
	}
	config, err := json.Marshal(map[string]any{
		"host": u.Hostname(), "port": port, "database": strings.TrimPrefix(u.Path, "/"),
		"username": u.User.Username(), "password": password, "sslmode": sslmode, "editor_role": "platform_editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := metadataDB(t)
	cipher, err := connections.NewCipher("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Seal(config, []byte(testkit.DBPG))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meta.Exec(`UPDATE database_connections SET active=false WHERE id<>$1`, testkit.DBPG); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.Exec(`UPDATE database_connections SET driver='postgres',config_encrypted=$2,migration_ready=false WHERE id=$1`, testkit.DBPG, sealed); err != nil {
		t.Fatal(err)
	}
	reg := connections.NewRegistry(meta, cipher, connections.RegistryOptions{})
	t.Cleanup(reg.Close)
	service := connections.NewService(meta, cipher, reg, LedgerInitializer{}, nil, NewStore(meta), connections.ServiceOptions{
		Features: connections.Features{Runner: map[database.Driver]bool{database.Postgres: true}},
	})
	ready, err := service.Test(t.Context(), testkit.DBPG)
	if err != nil || !ready.Capabilities.TransactionalMigrations {
		t.Fatal("PostgreSQL ledger readiness", err)
	}
	h, release, err := reg.Acquire(t.Context(), testkit.DBPG)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !h.MigrationReady {
		t.Fatal("registry did not retain readiness")
	}
	s := NewService(meta, reg, postgres.Inspector{}, testkit.NewFixtureAccessChecker(), platform.SystemClock{}, Options{Timeout: 5 * time.Second})
	t.Cleanup(func() {
		rows, err := meta.Query(`SELECT id FROM migration_runs`)
		if err != nil {
			t.Error(err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Error(err)
				return
			}
			if _, err := h.DB.Exec(`DELETE FROM _platform.migration_ledger WHERE run_id=$1`, id); err != nil {
				t.Error(err)
			}
		}
		if err := rows.Err(); err != nil {
			t.Error(err)
		}
	})
	return s, h
}

type postgresExecution struct {
	run MigrationRun
	err error
}

func postgresAsync(t *testing.T, fn func(context.Context) (MigrationRun, error)) <-chan postgresExecution {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	results := make(chan postgresExecution, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run, err := fn(ctx)
		results <- postgresExecution{run, err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("PostgreSQL operation did not stop")
		}
	})
	return results
}

func postgresResult(t *testing.T, results <-chan postgresExecution) postgresExecution {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(7 * time.Second):
		t.Fatal("PostgreSQL operation did not finish")
		return postgresExecution{}
	}
}

func postgresLock(t *testing.T, h database.TargetHandle) *targetTransaction {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	tx, err := beginTarget(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.close)
	return tx
}

func waitPostgresLock(t *testing.T, h database.TargetHandle) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		err := h.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory'
		 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
		 AND classid::bigint=$1 AND objid::bigint=$2 AND objsubid=1 AND NOT granted)`, targetLockKey>>32, targetLockKey&0xffffffff).Scan(&waiting)
		if err != nil {
			t.Fatal("inspect target lock", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("runner did not wait for the PostgreSQL transaction lock")
		case <-tick.C:
		}
	}
}

func postgresCount(t *testing.T, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count: want %d, got %d: %v", want, got, err)
	}
}

func assertPostgresCommit(t *testing.T, s *Service, h database.TargetHandle, d MigrationDraft, r MigrationRun) {
	t.Helper()
	if r.Status != Succeeded || r.Version == nil || *r.Version != d.ExpectedVersion+1 || r.FinishedAt == nil || r.Error != nil {
		t.Fatalf("incorrect committed run: %+v", r)
	}
	var version int64
	var hash, snapshot, actor, username string
	var at time.Time
	err := h.DB.QueryRowContext(t.Context(), `SELECT version,sql_hash,sql_snapshot,actor_id,actor_name_snapshot,applied_at
	 FROM _platform.migration_ledger WHERE run_id=$1`, r.ID).Scan(&version, &hash, &snapshot, &actor, &username, &at)
	if err != nil || version != *r.Version || hash != sqlHash(d.SQL) || snapshot != d.SQL || actor != editor.UserID || username != editor.Username {
		t.Fatal("ledger differs from committed input", err)
	}
	stored, err := s.GetRun(t.Context(), viewerPrincipal, h.DatabaseID, r.ID)
	if err != nil || stored.Status != Succeeded || stored.SQL != snapshot || stored.SQLHash != hash || stored.Actor.ID != actor || stored.Actor.Username != username || stored.FinishedAt == nil || stored.FinishedAt.Before(at) {
		t.Fatal("metadata differs from ledger", err)
	}
	applied, err := s.GetDraft(t.Context(), editor, h.DatabaseID, d.ID)
	if err != nil || applied.AppliedAt == nil || !applied.AppliedAt.Equal(*stored.FinishedAt) {
		t.Fatal("applied draft differs from ledger", err)
	}
	postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 1, r.ID)
}

func TestPostgresConcurrentExecute(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		name := "different keys"
		if sameKey {
			name = "same key"
		}
		t.Run(name, func(t *testing.T) {
			s, h := postgresRegistryService(t)
			table := uniqueTable(t, h)
			d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER); INSERT INTO `+table+` VALUES(1);`)
			in := input(d)
			lock := postgresLock(t, h)
			start := make(chan struct{})
			results := make(chan postgresExecution, 8)
			for i := 0; i < cap(results); i++ {
				request := in
				if !sameKey && i > 0 {
					request = input(d)
				}
				postgresAsync(t, func(ctx context.Context) (MigrationRun, error) {
					select {
					case <-start:
					case <-ctx.Done():
						return MigrationRun{}, ctx.Err()
					}
					run, err := s.Execute(ctx, editor, request)
					results <- postgresExecution{run, err}
					return run, err
				})
			}
			close(start)
			waitPostgresLock(t, h)
			for i := 0; i < cap(results)-1; i++ {
				result := postgresResult(t, results)
				if !isCode(result.err, "RUN_IN_PROGRESS") && !(sameKey && result.err == nil && result.run.Status == Running) {
					t.Fatalf("concurrent request: %+v %v", result.run, result.err)
				}
			}
			runs, _, err := s.ListRuns(t.Context(), viewerPrincipal, h.DatabaseID, 50, 0)
			if err != nil || len(runs) != 1 || runs[0].Status != Running {
				t.Fatal("concurrent requests created multiple runs", err)
			}
			_, err = s.Reconcile(t.Context(), editor, h.DatabaseID, runs[0].ID)
			assertCode(t, err, "RUN_IN_PROGRESS")
			if err := lock.rollback(); err != nil {
				t.Fatal(err)
			}
			finished := postgresResult(t, results)
			if finished.err != nil {
				t.Fatal(finished.err)
			}
			assertPostgresCommit(t, s, h, d, finished.run)
			postgresCount(t, h.DB, `SELECT count(*) FROM `+table, 1)
			postgresCount(t, s.store.DB, `SELECT count(*) FROM migration_runs`, 1)
			if sameKey {
				retry, err := s.Execute(t.Context(), editor, in)
				if err != nil || retry.ID != finished.run.ID || retry.Status != Succeeded {
					t.Fatal("duplicate changed committed outcome", err)
				}
			}
		})
	}
}

func TestPostgresActiveRunInputConflicts(t *testing.T) {
	s, h := postgresRegistryService(t)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER);`)
	other := create(t, s, h, "SELECT 1;")
	in := input(d)
	lock := postgresLock(t, h)
	results := postgresAsync(t, func(ctx context.Context) (MigrationRun, error) { return s.Execute(ctx, editor, in) })
	waitPostgresLock(t, h)
	for _, changed := range []ExecuteInput{
		{in.DatabaseID, other.ID, in.Revision, in.ExpectedVersion, in.IdempotencyKey},
		{in.DatabaseID, in.DraftID, in.Revision + 1, in.ExpectedVersion, in.IdempotencyKey},
		{in.DatabaseID, in.DraftID, in.Revision, in.ExpectedVersion + 1, in.IdempotencyKey},
	} {
		_, err := s.Execute(t.Context(), editor, changed)
		assertCode(t, err, "IDEMPOTENCY_CONFLICT")
	}
	_, err := s.Execute(t.Context(), editor, input(other))
	assertCode(t, err, "RUN_IN_PROGRESS")
	_, err = s.UpdateDraft(t.Context(), editor, h.DatabaseID, d.ID, UpdateDraftInput{DraftInput: DraftInput{Title: d.Title, SQL: "SELECT 2;", ExpectedVersion: d.ExpectedVersion}, Revision: d.Revision})
	assertCode(t, err, "RUN_IN_PROGRESS")
	postgresCount(t, h.DB, `SELECT count(*) FROM pg_class WHERE oid=to_regclass($1)`, 0, table)
	if err := lock.rollback(); err != nil {
		t.Fatal(err)
	}
	finished := postgresResult(t, results)
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	assertPostgresCommit(t, s, h, d, finished.run)
	_, err = s.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "DRAFT_APPLIED")
	_, err = s.Execute(t.Context(), editor, input(other))
	assertCode(t, err, "VERSION_CONFLICT")
}

func TestPostgresStaleDraftRevision(t *testing.T) {
	s, h := postgresRegistryService(t)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER);`)
	updated, err := s.UpdateDraft(t.Context(), editor, h.DatabaseID, d.ID, UpdateDraftInput{DraftInput: DraftInput{Title: d.Title, SQL: d.SQL + ` INSERT INTO ` + table + ` VALUES(1);`, ExpectedVersion: d.ExpectedVersion}, Revision: d.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "REVISION_CONFLICT")
	wrongVersion := input(updated)
	wrongVersion.ExpectedVersion++
	_, err = s.Execute(t.Context(), editor, wrongVersion)
	assertCode(t, err, "VERSION_CONFLICT")
	postgresCount(t, s.store.DB, `SELECT count(*) FROM migration_runs`, 0)
	postgresCount(t, h.DB, `SELECT count(*) FROM pg_class WHERE oid=to_regclass($1)`, 0, table)
	run, err := s.Execute(t.Context(), editor, input(updated))
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresCommit(t, s, h, updated, run)
}

func TestPostgresHeadCheckedAfterTargetLock(t *testing.T) {
	s, h := postgresRegistryService(t)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER);`)
	lock := postgresLock(t, h)
	externalID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = h.DB.Exec(`DELETE FROM _platform.migration_ledger WHERE run_id=$1`, externalID) })
	if _, err := lock.ExecContext(t.Context(), `INSERT INTO _platform.migration_ledger
	 (version,run_id,sql_hash,sql_snapshot,actor_id,actor_name_snapshot,applied_at) VALUES($1,$2,$3,'SELECT 1;',$4,$5,now())`, d.ExpectedVersion+1, externalID, sqlHash("SELECT 1;"), editor.UserID, editor.Username); err != nil {
		t.Fatal(err)
	}
	results := postgresAsync(t, func(ctx context.Context) (MigrationRun, error) { return s.Execute(ctx, editor, input(d)) })
	waitPostgresLock(t, h)
	if err := lock.commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	finished := postgresResult(t, results)
	assertCode(t, finished.err, "VERSION_CONFLICT")
	if finished.run.Status != Failed || finished.run.Version != nil || finished.run.CandidateVersion != d.ExpectedVersion+1 {
		t.Fatal("stale run advanced committed version")
	}
	postgresCount(t, h.DB, `SELECT count(*) FROM pg_class WHERE oid=to_regclass($1)`, 0, table)
	postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 0, finished.run.ID)
}

func TestPostgresTargetLockTimeoutAndCleanup(t *testing.T) {
	s, h := postgresRegistryService(t)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER);`)
	lock := postgresLock(t, h)
	s.timeout = 200 * time.Millisecond
	started := time.Now()
	run, err := s.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "TARGET_TIMEOUT")
	if run.Status != Failed || run.Version != nil || time.Since(started) > 2*time.Second {
		t.Fatal("target lock ignored timeout or changed committed version")
	}
	postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 0, run.ID)
	if err := lock.rollback(); err != nil {
		t.Fatal(err)
	}
	lock.close()
	h.DB.SetMaxOpenConns(1)
	s.timeout = 5 * time.Second
	committed, err := s.Execute(t.Context(), editor, input(d))
	if err != nil {
		t.Fatal("target connection or local lease leaked", err)
	}
	assertPostgresCommit(t, s, h, d, committed)
	var current, session, timeout string
	if err := h.DB.QueryRow(`SELECT current_user,session_user,current_setting('statement_timeout')`).Scan(&current, &session, &timeout); err != nil || current != session || timeout != "0" || h.DB.Stats().InUse != 0 {
		t.Fatal("transaction settings or role leaked", current, session, timeout, err)
	}
}

func TestPostgresStartupRecoversCommittedMetadataFailure(t *testing.T) {
	for _, persisted := range []string{OutcomeUnknown, Running} {
		t.Run(persisted, func(t *testing.T) {
			s, h := postgresRegistryService(t)
			table := uniqueTable(t, h)
			d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER); INSERT INTO `+table+` VALUES(1);`)
			condition := "NEW.status='succeeded'"
			if persisted == Running {
				condition = "true"
			}
			if _, err := s.store.DB.Exec(`CREATE FUNCTION reject_finalization() RETURNS trigger LANGUAGE plpgsql AS $$
			 BEGIN IF ` + condition + ` THEN RAISE EXCEPTION 'test metadata write unavailable'; END IF; RETURN NEW; END; $$;
			 CREATE TRIGGER reject_finalization BEFORE UPDATE ON migration_runs FOR EACH ROW EXECUTE FUNCTION reject_finalization()`); err != nil {
				t.Fatal(err)
			}
			in := input(d)
			run, err := s.Execute(t.Context(), editor, in)
			if err != nil || run.Status != OutcomeUnknown || run.Version != nil || run.FinishedAt != nil {
				t.Fatal("metadata failure claimed a terminal outcome", err)
			}
			stored, err := s.GetRun(t.Context(), viewerPrincipal, h.DatabaseID, run.ID)
			if err != nil || stored.Status != persisted {
				t.Fatal("unexpected persisted unresolved status", stored.Status, err)
			}
			postgresCount(t, h.DB, `SELECT count(*) FROM `+table, 1)
			postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 1, run.ID)
			applied, err := s.GetDraft(t.Context(), editor, h.DatabaseID, d.ID)
			if err != nil || applied.AppliedAt != nil {
				t.Fatal("failed metadata transaction partially finalized draft", err)
			}
			_, err = s.Execute(t.Context(), editor, input(d))
			assertCode(t, err, "TARGET_OUTCOME_UNKNOWN")
			retry, err := s.Execute(t.Context(), editor, in)
			if err != nil || retry.ID != run.ID || retry.Status != persisted {
				t.Fatal("unresolved idempotency retry changed run", err)
			}
			if _, err := s.store.DB.Exec(`DROP TRIGGER reject_finalization ON migration_runs`); err != nil {
				t.Fatal(err)
			}
			restarted := NewService(s.store.DB, s.provider, s.inspector, s.access, s.clock, Options{})
			for i := 0; i < 2; i++ {
				if err := restarted.Recover(t.Context()); err != nil {
					t.Fatal("startup recovery", err)
				}
			}
			recovered, err := restarted.Execute(t.Context(), editor, in)
			if err != nil || recovered.ID != run.ID || !recovered.StartedAt.Equal(stored.StartedAt) {
				t.Fatal("startup recovery changed original run", err)
			}
			assertPostgresCommit(t, restarted, h, d, recovered)
			postgresCount(t, h.DB, `SELECT count(*) FROM `+table, 1)
			postgresCount(t, s.store.DB, `SELECT count(*) FROM migration_runs`, 1)
		})
	}
}

func TestPostgresRecoveryWithoutLedgerNeverExecutesSQL(t *testing.T) {
	s, h := postgresRegistryService(t)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER);`)
	in := input(d)
	run, _, _, err := s.createRun(t.Context(), editor, in, inputHash(in))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`UPDATE database_connections SET migration_ready=false WHERE id=$1`, h.DatabaseID); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(s.store.DB, s.provider, s.inspector, s.access, s.clock, Options{})
	if err := restarted.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	unknown, err := restarted.GetRun(t.Context(), viewerPrincipal, h.DatabaseID, run.ID)
	if err != nil || unknown.Status != OutcomeUnknown {
		t.Fatal("uninspectable target was marked rolled back", err)
	}
	_, err = restarted.Execute(t.Context(), editor, input(d))
	assertCode(t, err, "TARGET_OUTCOME_UNKNOWN")
	if _, err := s.store.DB.Exec(`UPDATE database_connections SET migration_ready=true WHERE id=$1`, h.DatabaseID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Execute(t.Context(), editor, in)
	if err != nil || recovered.ID != run.ID || recovered.Status != Failed || recovered.Version != nil || recovered.FinishedAt == nil || recovered.Error == nil || recovered.Error.Code != "EXECUTION_INTERRUPTED" {
		t.Fatal("interrupted run was not finalized", err)
	}
	postgresCount(t, h.DB, `SELECT count(*) FROM pg_class WHERE oid=to_regclass($1)`, 0, table)
	postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 0, run.ID)
	applied, err := s.GetDraft(t.Context(), editor, h.DatabaseID, d.ID)
	if err != nil || applied.AppliedAt != nil {
		t.Fatal("interrupted draft marked applied", err)
	}
}

func TestPostgresReconcileWaitsForUncommittedLedger(t *testing.T) {
	s, h := postgresRegistryService(t)
	table := uniqueTable(t, h)
	d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER); INSERT INTO `+table+` VALUES(1);`)
	in := input(d)
	run, _, _, err := s.createRun(t.Context(), editor, in, inputHash(in))
	if err != nil {
		t.Fatal(err)
	}
	lock := postgresLock(t, h)
	statements, err := postgresStatements(d.SQL)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.apply(t.Context(), lock, h, run, d, statements); err != nil {
		t.Fatal(err)
	}
	results := postgresAsync(t, func(ctx context.Context) (MigrationRun, error) { return s.Reconcile(ctx, editor, h.DatabaseID, run.ID) })
	waitPostgresLock(t, h)
	postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 0, run.ID)
	select {
	case result := <-results:
		t.Fatalf("reconcile decided before target commit: %+v", result)
	default:
	}
	if err := lock.commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := postgresResult(t, results)
	if recovered.err != nil {
		t.Fatal(recovered.err)
	}
	assertPostgresCommit(t, s, h, d, recovered.run)
	postgresCount(t, h.DB, `SELECT count(*) FROM `+table, 1)
}

func TestPostgresReconcileRejectsLedgerMismatch(t *testing.T) {
	for _, field := range []string{"sql_hash", "version"} {
		t.Run(field, func(t *testing.T) {
			s, h := postgresRegistryService(t)
			table := uniqueTable(t, h)
			d := create(t, s, h, `CREATE TABLE `+table+`(id INTEGER); INSERT INTO `+table+` VALUES(1);`)
			in := input(d)
			run, _, _, err := s.createRun(t.Context(), editor, in, inputHash(in))
			if err != nil {
				t.Fatal(err)
			}
			lock := postgresLock(t, h)
			statements, err := postgresStatements(d.SQL)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.apply(t.Context(), lock, h, run, d, statements); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE _platform.migration_ledger SET version=version+1 WHERE run_id=$1`
			if field == "sql_hash" {
				query = `UPDATE _platform.migration_ledger SET sql_hash='sha256:` + strings.Repeat("0", 64) + `' WHERE run_id=$1`
			}
			if _, err := lock.ExecContext(t.Context(), query, run.ID); err != nil {
				t.Fatal(err)
			}
			if err := lock.commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			unknown, err := s.Reconcile(t.Context(), editor, h.DatabaseID, run.ID)
			assertCode(t, err, "INTERNAL_ERROR")
			if unknown.Status != OutcomeUnknown || unknown.Version != nil || unknown.FinishedAt != nil {
				t.Fatal("inconsistent ledger did not return an unknown outcome", unknown.Status)
			}
			stored, err := s.GetRun(t.Context(), viewerPrincipal, h.DatabaseID, run.ID)
			if err != nil || stored.Status != unknown.Status {
				t.Fatal("reconcile response differs from stored outcome", err)
			}
			_, err = s.Execute(t.Context(), editor, input(d))
			assertCode(t, err, "TARGET_OUTCOME_UNKNOWN")
			retry, err := s.Execute(t.Context(), editor, in)
			if err != nil || retry.ID != run.ID || retry.Status != OutcomeUnknown {
				t.Fatal("inconsistent run was retried", err)
			}
			assertCode(t, s.Recover(t.Context()), "INTERNAL_ERROR")
			postgresCount(t, h.DB, `SELECT count(*) FROM `+table, 1)
			postgresCount(t, h.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, 1, run.ID)
		})
	}
}
