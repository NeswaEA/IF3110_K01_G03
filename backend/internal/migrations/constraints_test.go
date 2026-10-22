package migrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunConstraintsAndImmutability(t *testing.T) {
	h := sqliteTarget(t)
	s, _ := migrationService(t, h)
	d := create(t, s, h, "CREATE TABLE constraints_probe(id INTEGER);")
	in := input(d)
	r, _, _, err := s.createRun(t.Context(), editor, in, inputHash(in))
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{
		"SQL snapshot":            `UPDATE migration_runs SET sql_snapshot='changed' WHERE id=$1`,
		"actor":                   `UPDATE migration_runs SET actor_name_snapshot='forged' WHERE id=$1`,
		"candidate version":       `UPDATE migration_runs SET candidate_version=candidate_version+1 WHERE id=$1`,
		"version before success":  `UPDATE migration_runs SET version=1 WHERE id=$1`,
		"missing success version": `UPDATE migration_runs SET status='succeeded',finished_at=now() WHERE id=$1`,
		"missing failure error":   `UPDATE migration_runs SET status='failed',finished_at=now() WHERE id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.store.DB.Exec(query, r.ID); err == nil {
				t.Fatal("invalid run accepted")
			}
		})
	}
	if _, err := s.Reconcile(t.Context(), editor, h.DatabaseID, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`UPDATE migration_runs SET status='running',finished_at=null,error_code=null,error_message=null WHERE id=$1`, r.ID); err == nil {
		t.Fatal("terminal run restarted")
	}
	if _, err := s.store.DB.Exec(`DELETE FROM migration_drafts WHERE id=$1`, d.ID); err == nil {
		t.Fatal("history draft deleted")
	}
	if _, err := s.store.DB.Exec(`UPDATE migration_drafts SET expected_version=9007199254740991 WHERE id=$1`, d.ID); err == nil {
		t.Fatal("version overflow accepted")
	}
}

func TestMigrationFixtures(t *testing.T) {
	for _, status := range []string{Running, Succeeded, Failed, OutcomeUnknown} {
		name := status
		if status == OutcomeUnknown {
			name = "unknown"
		}
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "fixtures", "api", "migrations_run_"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Data MigrationRun `json:"data"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		r := payload.Data
		if r.Status != status || r.SQLHash != sqlHash(r.SQL) {
			t.Fatal("fixture status or SQL hash differs")
		}
		if (r.Version != nil) != (status == Succeeded) || (r.FinishedAt == nil) != unresolved(r) || (r.Error != nil) != (status == Failed) {
			t.Fatal("fixture lifecycle fields disagree")
		}
	}
}
