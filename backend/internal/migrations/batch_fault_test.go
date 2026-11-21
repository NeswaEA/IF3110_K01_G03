package migrations

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit/pgfault"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestBatchCheckpointSurvivesLostCommitReply(t *testing.T) {
	ctx := t.Context()
	direct := pgTarget(t)
	if err := (HistoryInitializer{}).EnsureReady(ctx, direct); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(requireEnv(t, "TEST_TARGET_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid target DSN")
	}
	upstream := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	proxy := pgfault.New(t, upstream)
	host, port := proxy.HostPort()
	cfg.Host, cfg.Port = host, uint16(port)
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	target := direct
	target.DB = stdlib.OpenDB(*cfg)
	defer target.DB.Close()
	x := Executor{service: &Service{store: NewStore(nil, testkit.NewClock(time.Now()))},
		options: ExecutorOptions{BatchTimeout: time.Second, LockTimeout: time.Second}}

	for _, mode := range []pgfault.Fault{pgfault.DropUserStatement, pgfault.DropBeforeCommit, pgfault.DropCommitReply} {
		t.Run(string(mode), func(t *testing.T) {
			id := uuid.New()
			name := "public.fault_batch_" + strings.ReplaceAll(id.String(), "-", "")
			if _, err := direct.DB.ExecContext(ctx, "CREATE TABLE "+name+" (id bigint PRIMARY KEY); GRANT INSERT,SELECT ON "+name+" TO platform_editor"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := direct.DB.Exec("DROP TABLE " + name); err != nil {
					t.Error(err)
				}
				if _, err := direct.DB.Exec("DELETE FROM _platform.migration_checkpoints WHERE execution_id=$1", id); err != nil {
					t.Error(err)
				}
			})
			lock, err := (postgres.Locker{}).Acquire(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()

			proxy.Arm(mode, name)
			e := executionRow{Execution: Execution{ID: id.String(), ChainRootID: id.String(), Direction: "up", Batch: &ExecutionBatch{BatchSize: 1}}}
			history := HistoryRecord{SQLExecuted: "INSERT INTO " + name + " SELECT $1::bigint+1 WHERE $1<$2 LIMIT $3 RETURNING id"}
			checkpoint, failure := x.commitBatch(ctx, normalTarget{conn: lock.Conn, target: target}, lock, e, &history, "", 0, 2, 1, 0, 0)
			if checkpoint != nil || failure == nil || (mode != pgfault.DropUserStatement && !failure.uncertain) {
				t.Fatalf("lost session must not report a confirmed checkpoint: %+v, %+v", checkpoint, failure)
			}
			committed := mode == pgfault.DropCommitReply
			select {
			case event := <-proxy.Events():
				if event.Mode != mode || event.CommitForwarded != committed || event.CommitConfirmed != committed {
					t.Fatalf("unexpected proxy event: %+v", event)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("proxy did not observe the requested failure")
			}
			if proxy.UserQueries() != 1 {
				t.Fatal("the failed attempt should have sent exactly one batch query")
			}
			proxy.Restore()

			var rows, checkpoints int
			if err := direct.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+name).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if err := direct.DB.QueryRowContext(ctx, "SELECT count(*) FROM _platform.migration_checkpoints WHERE execution_id=$1", id).Scan(&checkpoints); err != nil {
				t.Fatal(err)
			}
			want := 0
			if committed {
				want = 1
			}
			if rows != want || checkpoints != want {
				t.Fatalf("data and checkpoint must commit together: rows=%d, checkpoints=%d, want=%d", rows, checkpoints, want)
			}
		})
	}
}
