package webui_test

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

const desiredDatabaseID = "30000000-0000-4000-8000-000000000003"

func diffSources(t *testing.T) *sources {
	s := newSources(t)
	desired := s.dbs[0]
	desired.ID, desired.Name, desired.EffectiveAccess = desiredDatabaseID, "Pembanding view", contracts.AccessView
	desired.Capabilities.TransactionalMigrations = false
	s.dbs = append(s.dbs, desired)
	return s
}

func TestSchemaDiffPageRendersChoicesAndBootstrap(t *testing.T) {
	s := diffSources(t)
	path := "/databases/" + databaseID + "/schema-diff?desired_database_id=" + desiredDatabaseID + "&schema=public"
	for i := 0; i < 2; i++ {
		status, body := (harness{t, s, viewerUser, nil}).get(path)
		if status != 200 {
			t.Fatal(status, body)
		}
		expect(t, body, []string{"Perbandingan schema: PostgreSQL latihan", `name="desired_database_id"`, `value="` + desiredDatabaseID + `" selected`, "Pembanding view", `value="public" selected`, `disabled>Bandingkan schema`, `/assets/schemadiff-1.js`}, "SQLite latihan", "secret-session-id")
		initial := initialMigrationState(t, body)
		var comparisons []connections.Database
		if err := json.Unmarshal(initial["comparisons"], &comparisons); err != nil || len(comparisons) != 1 || comparisons[0].ID != desiredDatabaseID || comparisons[0].EffectiveAccess != contracts.AccessView {
			t.Fatal("wrong comparisons", comparisons, err)
		}
		if string(initial["schema"]) != `"public"` {
			t.Fatal("selected schema missing")
		}
	}
}

func TestSchemaDiffPageRequiresEditBeforeTargetReads(t *testing.T) {
	for _, code := range []string{"UNAUTHENTICATED", "FORBIDDEN", "NOT_FOUND"} {
		s := diffSources(t)
		status, body := (harness{t, s, viewerUser, &contracts.DomainError{Code: code}}).get("/databases/" + databaseID + "/schema-diff")
		if code == "FORBIDDEN" && status != 403 || code == "NOT_FOUND" && status != 404 || code == "UNAUTHENTICATED" && status != 302 {
			t.Fatal(code, status)
		}
		if len(s.reads) > 0 || strings.Contains(body, "data-initial") {
			t.Fatal("denied page inspected target")
		}
	}
	s := diffSources(t)
	s.dbs[0].EffectiveAccess = contracts.AccessView
	status, _ := (harness{t, s, viewerUser, nil}).get("/databases/" + databaseID + "/schema-diff")
	if status != 403 || len(s.reads) != 0 {
		t.Fatal("read-only metadata exposed edit page", status)
	}
}

func TestSchemaDiffPageHandlesEmptyChoicesAndTargetProblems(t *testing.T) {
	for _, mode := range []string{"comparisons", "schemas", "capability", "readiness", "unreachable"} {
		t.Run(mode, func(t *testing.T) {
			s := diffSources(t)
			want := ""
			switch mode {
			case "comparisons":
				s.dbs = s.dbs[:1]
				want = "Belum ada database pembanding"
			case "schemas":
				s.snap.Schemas = nil
				want = "Tidak ada schema pengguna"
			case "capability":
				s.dbs[0].Capabilities.SchemaDiff = false
				want = "Schema diff belum tersedia"
			case "readiness":
				s.dbs[0].Capabilities.TransactionalMigrations = false
				want = "Admin perlu menguji koneksi source"
			case "unreachable":
				s.schemaErr = database.ErrTargetUnavailable
				want = "Target tidak dapat dihubungi"
			}
			status, body := (harness{t, s, viewerUser, nil}).get("/databases/" + databaseID + "/schema-diff")
			if status != 200 || !strings.Contains(body, want) {
				t.Fatal(status, body)
			}
			if (mode == "capability" || mode == "readiness") && len(s.reads) != 0 {
				t.Fatal("unavailable page opened target")
			}
		})
	}
}

func TestSchemaDiffPageValidatesQueryAndHidesUnavailableComparisons(t *testing.T) {
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"desired_database_id=invalid", 400}, {"schema=public&schema=public", 400}, {"desired_database_id=&desired_database_id=", 400},
		{"schema=missing", 404}, {"schema=%00", 400}, {"desired_database_id=" + databaseID, 404}, {"desired_database_id=30000000-0000-4000-8000-000000000002", 404},
	} {
		status, _ := (harness{t, diffSources(t), viewerUser, nil}).get("/databases/" + databaseID + "/schema-diff?" + tc.query)
		if status != tc.status {
			t.Fatal(tc.query, status)
		}
	}
	s := diffSources(t)
	s.listErr = errors.New("private metadata failure")
	status, body := (harness{t, s, viewerUser, nil}).get("/databases/" + databaseID + "/schema-diff")
	if status != 500 || strings.Contains(body, "private metadata failure") {
		t.Fatal(status, body)
	}
}

func TestSchemaDiffPageEscapesNamesAndDoesNotLeakDeniedComparisons(t *testing.T) {
	s := diffSources(t)
	attack := `</option><script>alert("private")</script>`
	s.dbs[2].Name = attack
	denied := s.dbs[2]
	denied.ID = "90000000-0000-4000-8000-000000000099"
	denied.Name = "denied database"
	denied.EffectiveAccess = contracts.AccessNone
	s.dbs = append(s.dbs, denied)
	status, body := (harness{t, s, viewerUser, nil}).get("/databases/" + databaseID + "/schema-diff")
	if status != http.StatusOK {
		t.Fatal(status, body)
	}
	expect(t, body, []string{html.EscapeString(attack)}, "<script>alert", "denied database")
}
