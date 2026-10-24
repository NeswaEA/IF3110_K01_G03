package webui_test

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/migrations"
)

type migrationSource struct {
	drafts                          []migrations.MigrationDraft
	runs                            []migrations.MigrationRun
	draftMore, runMore              bool
	draftError, runError, headError error
	draftReads, runReads, headReads int
	draftOffset, historyOffset      int
}

func newMigrationSource(t *testing.T) *migrationSource {
	return &migrationSource{
		drafts: []migrations.MigrationDraft{fixture[migrations.MigrationDraft](t, "migrations_draft.json")},
		runs:   []migrations.MigrationRun{fixture[migrations.MigrationRun](t, "migrations_run_succeeded.json")},
	}
}

func (s *migrationSource) ListDrafts(_ context.Context, _ contracts.Principal, _ string, _, offset int) ([]migrations.MigrationDraft, bool, error) {
	s.draftReads++
	s.draftOffset = offset
	return s.drafts, s.draftMore, s.draftError
}

func (s *migrationSource) GetDraft(_ context.Context, _ contracts.Principal, db, id string) (migrations.MigrationDraft, error) {
	for _, draft := range s.drafts {
		if draft.DatabaseID == db && draft.ID == id {
			return draft, nil
		}
	}
	return migrations.MigrationDraft{}, &contracts.DomainError{Code: "NOT_FOUND"}
}

func (s *migrationSource) ListRuns(_ context.Context, _ contracts.Principal, _ string, _, offset int) ([]migrations.MigrationRun, bool, error) {
	s.runReads++
	s.historyOffset = offset
	return s.runs, s.runMore, s.runError
}

func (s *migrationSource) GetRun(_ context.Context, _ contracts.Principal, db, id string) (migrations.MigrationRun, error) {
	for _, run := range s.runs {
		if run.DatabaseID == db && run.ID == id {
			return run, nil
		}
	}
	return migrations.MigrationRun{}, &contracts.DomainError{Code: "NOT_FOUND"}
}

func (s *migrationSource) Head(context.Context, contracts.Principal, string) (migrations.MigrationHead, error) {
	s.headReads++
	return migrations.MigrationHead{Version: 7}, s.headError
}

func initialMigrationState(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	attribute := regexp.MustCompile(`data-initial="([^"]*)"`).FindStringSubmatch(body)
	if len(attribute) != 2 {
		t.Fatal("missing initial state")
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal([]byte(html.UnescapeString(attribute[1])), &state); err != nil {
		t.Fatal("invalid initial state:", err)
	}
	return state
}

func TestMigrationsRenderMetadataAndSelectedResources(t *testing.T) {
	source := newMigrationSource(t)
	query := "?draft=" + source.drafts[0].ID + "&run=" + source.runs[0].ID
	status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations"+query, source)
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	expect(t, body, []string{
		"Migrasi: PostgreSQL latihan", "postgres · akses lihat dan edit", "Versi committed: 7",
		"<table>", "<h2>Draft revision 1</h2>", "<h2>Detail eksekusi</h2>", "Status: Berhasil",
		source.drafts[0].Title, html.EscapeString(source.drafts[0].SQL), html.EscapeString(source.runs[0].SQL),
		"#migration-draft", "#migration-run",
	}, "secret-session-id")
	initial := initialMigrationState(t, body)
	var draft migrations.MigrationDraft
	var run migrations.MigrationRun
	if err := json.Unmarshal(initial["draft"], &draft); err != nil || draft.ID != source.drafts[0].ID {
		t.Fatal("selected draft not passed to widget", err)
	}
	if err := json.Unmarshal(initial["run"], &run); err != nil || run.ID != source.runs[0].ID {
		t.Fatal("selected run not passed to widget", err)
	}
}

func TestMigrationsViewerNeverReadsDrafts(t *testing.T) {
	src := newSources(t)
	src.dbs[0].EffectiveAccess = contracts.AccessView
	source := newMigrationSource(t)
	source.draftError = errors.New("draft access must not happen")
	path := "/databases/" + databaseID + "/migrations"
	h := harness{t, src, viewerUser, nil}
	status, body := h.getMigrations(path+"?run="+source.runs[0].ID, source)
	if status != 200 || source.draftReads != 0 {
		t.Fatal(status, source.draftReads, body)
	}
	expect(t, body, []string{"Akses lihat hanya dapat membaca riwayat migrasi.", "<table>", "Status: Berhasil"}, "Draft SQL", source.drafts[0].Title)
	if string(initialMigrationState(t, body)["drafts"]) != "null" {
		t.Fatal("viewer bootstrap contains drafts")
	}
	status, _ = h.getMigrations(path+"?draft="+source.drafts[0].ID, source)
	if status != http.StatusForbidden || source.draftReads != 0 || source.headReads != 1 {
		t.Fatal("viewer draft selection did not stop before resource reads", status)
	}
}

func TestMigrationsKeepHistoryWhenHeadFails(t *testing.T) {
	for _, err := range []error{
		&contracts.DomainError{Code: "TARGET_UNAVAILABLE", Message: "postgres://secret@host"},
		&contracts.DomainError{Code: "TARGET_NOT_READY"},
		errors.New("private engine error"),
	} {
		source := newMigrationSource(t)
		source.headError = err
		status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations", source)
		if status != http.StatusOK {
			t.Fatal(status, body)
		}
		expect(t, body, []string{"Riwayat tersimpan tetap tersedia.", "<table>", "Berhasil"}, "postgres://secret", "private engine error")
		initial := initialMigrationState(t, body)
		if string(initial["head"]) != "null" || string(initial["head_error"]) == `""` {
			t.Fatal("head error missing in bootstrap")
		}
	}
}

func TestMigrationsEscapeSQLAndBootstrap(t *testing.T) {
	source := newMigrationSource(t)
	attack := `SELECT '</section><script>alert("private")</script>&';`
	source.drafts[0].Title, source.drafts[0].SQL = attack, attack
	source.runs[0].SQL = attack
	source.runs[0].Actor.Username = `<img src=x onerror=alert(1)>`
	source.runs[0].Error = &migrations.RunError{Code: "SQL_EXECUTION_FAILED", Message: attack}
	path := "/databases/" + databaseID + "/migrations?draft=" + source.drafts[0].ID + "&run=" + source.runs[0].ID
	status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations(path, source)
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	expect(t, body, []string{"&lt;script&gt;", "&lt;img src=x"}, "<script>alert", "<img src=x", "#ZgotmplZ")
	var draft migrations.MigrationDraft
	if err := json.Unmarshal(initialMigrationState(t, body)["draft"], &draft); err != nil || draft.SQL != attack {
		t.Fatal("escaped bootstrap did not preserve exact SQL", err)
	}
}

func TestMigrationsPaginationPreservesSelection(t *testing.T) {
	source := newMigrationSource(t)
	source.draftMore, source.runMore = true, true
	path := "/databases/" + databaseID + "/migrations?draft=" + source.drafts[0].ID + "&run=" + source.runs[0].ID
	status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations(path+"&draft_offset=50&history_offset=100", source)
	if status != 200 || source.draftOffset != 50 || source.historyOffset != 100 {
		t.Fatal(status, source.draftOffset, source.historyOffset)
	}
	decoded := html.UnescapeString(body)
	for _, fragment := range []string{"draft_offset=100&history_offset=100&run=", "draft_offset=50&history_offset=150&run=", "draft_offset=50&history_offset=50&run="} {
		if !strings.Contains(decoded, fragment) {
			t.Fatal("missing page link", fragment)
		}
	}
	if !strings.Contains(decoded, "draft="+source.drafts[0].ID+"&draft_offset=100") {
		t.Fatal("pagination lost selected draft")
	}
	_, body = (harness{t, newSources(t), viewerUser, nil}).getMigrations(path+"&draft_offset=100000&history_offset=100000", source)
	if strings.Contains(body, "offset=100050") {
		t.Fatal("next link exceeded maximum offset")
	}
}

func TestMigrationsValidateQueryBeforeReadingResources(t *testing.T) {
	for _, query := range []string{
		"draft=invalid", "run=invalid", "draft_offset=-1", "history_offset=100001", "draft_offset=1.5",
		"history_offset=999999999999999999999999", "draft_offset=0&draft_offset=50", "run=&run=",
	} {
		t.Run(query, func(t *testing.T) {
			source := newMigrationSource(t)
			status, _ := (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations?"+query, source)
			if status != http.StatusBadRequest || source.draftReads+source.runReads+source.headReads != 0 {
				t.Fatal(status, "invalid query reached migration service")
			}
		})
	}
}

func TestMigrationsRejectMissingAndForeignResources(t *testing.T) {
	for _, selected := range []string{"draft", "run"} {
		for _, foreign := range []bool{false, true} {
			source := newMigrationSource(t)
			id := "90000000-0000-4000-8000-000000000001"
			if foreign {
				if selected == "draft" {
					id = source.drafts[0].ID
					source.drafts[0].DatabaseID = "another-database"
				} else {
					id = source.runs[0].ID
					source.runs[0].DatabaseID = "another-database"
				}
			}
			status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations?"+selected+"="+id, source)
			if status != http.StatusNotFound || strings.Contains(body, "data-initial") {
				t.Fatal("missing/foreign resource rendered", status)
			}
		}
	}
}

func TestMigrationsDoNotTreatMetadataErrorsAsEmpty(t *testing.T) {
	for _, draftError := range []bool{false, true} {
		source := newMigrationSource(t)
		if draftError {
			source.draftError = &contracts.DomainError{Code: "FORBIDDEN"}
		} else {
			source.runError = &contracts.DomainError{Code: "FORBIDDEN"}
		}
		status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations", source)
		if status != http.StatusForbidden || strings.Contains(body, "Belum ada") {
			t.Fatal("denied metadata became an empty list", status)
		}
	}
}

func TestMigrationsEmptyAndUnavailableSource(t *testing.T) {
	source := &migrationSource{}
	status, body := (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations", source)
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	expect(t, body, []string{"Belum ada draft.", "Belum ada eksekusi migrasi."})
	if strings.Contains(string(initialMigrationState(t, body)["history"]), `"data":null`) {
		t.Fatal("empty history must be an array")
	}
	status, _ = (harness{t, newSources(t), viewerUser, nil}).getMigrations("/databases/"+databaseID+"/migrations", nil)
	if status != http.StatusInternalServerError {
		t.Fatal("missing service silently rendered an empty page", status)
	}
}
