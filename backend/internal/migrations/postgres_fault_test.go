package migrations

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func postgresFaultService(t *testing.T) (*Service, database.TargetHandle, *postgresFaultProxy) {
	t.Helper()
	observer := pgTarget(t)
	u, err := url.Parse(requireEnv(t, "TEST_TARGET_DATABASE_URL"))
	if err != nil || u.Query().Get("sslmode") != "disable" {
		t.Fatal("fault tests require an isolated PostgreSQL target with sslmode=disable")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	proxy := newPostgresFaultProxy(t, net.JoinHostPort(u.Hostname(), port))
	u.Host = proxy.listener.Addr().String()
	t.Setenv("TEST_TARGET_DATABASE_URL", u.String())
	s, _ := postgresRegistryService(t)
	t.Cleanup(proxy.restore)
	return s, observer, proxy
}

func TestPostgresConnectionLoss(t *testing.T) {
	for _, mode := range []postgresFault{dropUserStatement, dropBeforeCommit, dropCommitReply} {
		for _, startup := range []bool{false, true} {
			name := string(mode) + "/reconcile"
			if startup {
				name = string(mode) + "/startup"
			}
			t.Run(name, func(t *testing.T) {
				s, target, proxy := postgresFaultService(t)
				table := uniqueTable(t, target)
				draft := create(t, s, target, `CREATE TABLE `+table+`(id INTEGER PRIMARY KEY); INSERT INTO `+table+` VALUES(1);`)
				marker := strings.Trim(strings.TrimPrefix(table, "public."), `"`)
				proxy.arm(mode, marker)
				in := input(draft)
				results := postgresAsync(t, func(ctx context.Context) (MigrationRun, error) { return s.Execute(ctx, editor, in) })
				select {
				case event := <-proxy.events:
					if event.mode != mode || event.commitForwarded != (mode == dropCommitReply) || event.commitConfirmed != (mode == dropCommitReply) {
						t.Fatalf("incorrect network fault: %+v", event)
					}
				case <-time.After(4 * time.Second):
					t.Fatal("PostgreSQL fault point was not reached")
				}
				finished := postgresResult(t, results)
				unknown := finished.run
				if finished.err != nil || unknown.Status != OutcomeUnknown || unknown.Version != nil || unknown.FinishedAt != nil || unknown.Error != nil {
					t.Fatalf("connection loss claimed a terminal outcome: %+v %v", unknown, finished.err)
				}
				if proxy.userQueries() != 2 {
					t.Fatal("SQL did not reach the intended fault point", proxy.userQueries())
				}
				stored, err := s.GetRun(t.Context(), viewerPrincipal, target.DatabaseID, unknown.ID)
				if err != nil || stored.Status != OutcomeUnknown || stored.SQL != draft.SQL || stored.SQLHash != sqlHash(draft.SQL) {
					t.Fatal("connection loss was not persisted correctly", err)
				}
				postgresCount(t, s.store.DB, `SELECT count(*) FROM migration_runs`, 1)
				committed := 0
				if mode == dropCommitReply {
					committed = 1
					postgresCount(t, target.DB, `SELECT count(*) FROM `+table, 1)
				}
				postgresCount(t, target.DB, `SELECT count(*) FROM pg_class WHERE oid=to_regclass($1)`, committed, table)
				postgresCount(t, target.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, committed, unknown.ID)

				recovery := s
				if startup {
					recovery = NewService(s.store.DB, s.provider, s.inspector, s.access, s.clock, Options{})
				}
				resolve := func() (MigrationRun, error) {
					if startup {
						if err := recovery.Recover(t.Context()); err != nil {
							return MigrationRun{}, err
						}
						return recovery.GetRun(t.Context(), viewerPrincipal, target.DatabaseID, unknown.ID)
					}
					return recovery.Reconcile(t.Context(), editor, target.DatabaseID, unknown.ID)
				}
				stillUnknown, err := resolve()
				if err != nil || stillUnknown.Status != OutcomeUnknown || stillUnknown.Version != nil {
					t.Fatal("offline target was incorrectly resolved", err)
				}
				_, err = recovery.Execute(t.Context(), editor, input(draft))
				assertCode(t, err, "TARGET_OUTCOME_UNKNOWN")
				retry, err := recovery.Execute(t.Context(), editor, in)
				if err != nil || retry.ID != unknown.ID || retry.Status != OutcomeUnknown {
					t.Fatal("unknown idempotency retry changed the run", err)
				}
				if proxy.userQueries() != 2 {
					t.Fatal("SQL was retried while the target was offline")
				}

				proxy.restore()
				for i := 0; i < 2; i++ {
					recovered, err := resolve()
					if err != nil || recovered.ID != unknown.ID || recovered.SQL != draft.SQL || recovered.CandidateVersion != draft.ExpectedVersion+1 || !recovered.StartedAt.Equal(stored.StartedAt) {
						t.Fatal("recovery changed immutable run input", err)
					}
					if committed == 1 {
						assertPostgresCommit(t, recovery, target, draft, recovered)
						postgresCount(t, target.DB, `SELECT count(*) FROM `+table, 1)
					} else {
						if recovered.Status != Failed || recovered.Version != nil || recovered.FinishedAt == nil || recovered.Error == nil || recovered.Error.Code != "EXECUTION_INTERRUPTED" {
							t.Fatal("rolled-back target was not confirmed by reconciliation")
						}
						applied, err := recovery.GetDraft(t.Context(), editor, target.DatabaseID, draft.ID)
						if err != nil || applied.AppliedAt != nil {
							t.Fatal("rolled-back draft was marked applied", err)
						}
					}
				}
				retry, err = recovery.Execute(t.Context(), editor, in)
				if err != nil || retry.ID != unknown.ID || unresolved(retry) || proxy.userQueries() != 2 {
					t.Fatal("recovery or retry replayed SQL", err)
				}
				head, err := recovery.Head(t.Context(), viewerPrincipal, target.DatabaseID)
				if err != nil || head.Version != draft.ExpectedVersion+int64(committed) {
					t.Fatal("connection loss changed committed head incorrectly", err)
				}
				postgresCount(t, target.DB, `SELECT count(*) FROM pg_class WHERE oid=to_regclass($1)`, committed, table)
				postgresCount(t, target.DB, `SELECT count(*) FROM _platform.migration_ledger WHERE run_id=$1`, committed, unknown.ID)
				postgresCount(t, s.store.DB, `SELECT count(*) FROM migration_runs`, 1)

				next := create(t, recovery, target, "SELECT 1;")
				nextRun, err := recovery.Execute(t.Context(), editor, input(next))
				if err != nil {
					t.Fatal("network fault leaked a connection or execution lease", err)
				}
				assertPostgresCommit(t, recovery, target, next, nextRun)
				handle, release, err := recovery.provider.Acquire(t.Context(), target.DatabaseID)
				if err != nil {
					t.Fatal(err)
				}
				release()
				var current, session, timeout string
				if err := handle.DB.QueryRow(`SELECT current_user,session_user,current_setting('statement_timeout')`).Scan(&current, &session, &timeout); err != nil || current != session || timeout != "0" || handle.DB.Stats().InUse != 0 {
					t.Fatal("network fault leaked transaction state", err)
				}
				if proxy.userQueries() != 2 {
					t.Fatal("later execution unexpectedly replayed the interrupted SQL")
				}
			})
		}
	}
}
