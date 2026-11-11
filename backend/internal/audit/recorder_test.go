package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/audit"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
)

func record(t *testing.T, ctx context.Context, e contracts.AuditEvent) (map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	audit.NewRecorder(platform.NewLogger(&buf, slog.LevelInfo, nil)).Record(ctx, e)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	return line, buf.String()
}

func TestRecordWritesOneAuditLine(t *testing.T) {
	id, name, db := "u1", "lead", "d1"
	line, _ := record(t, platform.WithActorID(context.Background(), id), contracts.AuditEvent{
		Action: "request.approved", ActorID: &id, ActorName: &name, DatabaseID: &db,
		ResourceType: "change_request", ResourceID: "r1",
		Details: map[string]any{"content_hash": "ab12", "revision": 3, "nested": map[string]any{"group_id": "g1"}},
	})
	details := line["details"].(map[string]any)
	if line["event"] != "audit" || line["level"] != "INFO" || line["action"] != "request.approved" || line["actor_id"] != id ||
		line["actor_name"] != name || line["database_id"] != db || line["resource_type"] != "change_request" || line["resource_id"] != "r1" ||
		details["content_hash"] != "ab12" || details["revision"] != float64(3) || details["nested"].(map[string]any)["group_id"] != "g1" {
		t.Fatal("incorrect audit line", line)
	}
}

func TestRecordOmitsMissingActorAndDatabase(t *testing.T) {
	line, _ := record(t, context.Background(), contracts.AuditEvent{Action: "connection.changed", ResourceType: "connection", ResourceID: "c1"})
	for _, key := range []string{"actor_id", "actor_name", "database_id"} {
		if _, ok := line[key]; ok {
			t.Fatal(key + " must be absent")
		}
	}
}

func TestRecordDropsSQLAndRedactsSecrets(t *testing.T) {
	_, out := record(t, context.Background(), contracts.AuditEvent{
		Action: "migration.updated", ResourceType: "migration", ResourceID: "m1",
		Details: map[string]any{
			"up_sql": "UPDATE accounts SET balance = 0", "Down_SQL": "SELECT 1", "revision": 2,
			"password": "hunter2", "connection": map[string]any{"dsn": "postgres://u:p@h/db", "name": "demo"},
		},
	})
	for _, leaked := range []string{"UPDATE accounts", "SELECT 1", "hunter2", "postgres://", "up_sql", "Down_SQL"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("%q leaked in %s", leaked, out)
		}
	}
	if !strings.Contains(out, `"revision":2`) || !strings.Contains(out, `"name":"demo"`) {
		t.Fatal("harmless details were removed", out)
	}
}

func TestRecordRejectsUnknownAction(t *testing.T) {
	line, _ := record(t, context.Background(), contracts.AuditEvent{Action: "made.up", ResourceType: "x", ResourceID: "1"})
	if line["event"] != "audit.rejected" || line["level"] != "ERROR" {
		t.Fatal("unknown action was logged as audit", line)
	}
}
