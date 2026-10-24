package schemadiff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestDiffHandlerValidationAndPermissions(t *testing.T) {
	valid := `{"desired_database_id":"` + testkit.DBSQLiteDesired + `","schema":"main"}`
	for _, tc := range []struct {
		name, body string
		user       contracts.Principal
		status     int
		code       string
	}{
		{"anonymous", valid, contracts.Principal{}, 401, "UNAUTHENTICATED"},
		{"view before malformed body", "bad JSON", contracts.Principal{UserID: testkit.UserView}, 403, "FORBIDDEN"},
		{"admin without grant", valid, contracts.Principal{UserID: testkit.UserAdmin, IsPlatformAdmin: true}, 404, "NOT_FOUND"},
		{"missing desired", `{"schema":"main"}`, principal, 400, "INVALID_REQUEST"},
		{"missing schema", `{"desired_database_id":"` + testkit.DBSQLiteDesired + `"}`, principal, 400, "INVALID_REQUEST"},
		{"null", `{"schema":null,"desired_database_id":null}`, principal, 400, "INVALID_REQUEST"},
		{"empty", `{}`, principal, 400, "INVALID_REQUEST"},
		{"unknown", `{"desired_database_id":"` + testkit.DBSQLiteDesired + `","schema":"main","sql":"SELECT 1"}`, principal, 400, "INVALID_REQUEST"},
		{"trailing", valid + `{}`, principal, 400, "INVALID_REQUEST"},
		{"array", `[]`, principal, 400, "INVALID_REQUEST"},
		{"invalid desired", `{"desired_database_id":"bad","schema":"main"}`, principal, 400, "INVALID_REQUEST"},
		{"inaccessible desired", `{"desired_database_id":"90000000-0000-4000-8000-000000000099","schema":"main"}`, principal, 404, "NOT_FOUND"},
		{"empty schema", `{"desired_database_id":"` + testkit.DBSQLiteDesired + `","schema":""}`, principal, 400, "INVALID_REQUEST"},
		{"reserved", `{"desired_database_id":"` + testkit.DBSQLiteDesired + `","schema":"_platform"}`, principal, 404, "NOT_FOUND"},
		{"wrong schema", `{"desired_database_id":"` + testkit.DBSQLiteDesired + `","schema":"public"}`, principal, 422, "DIFF_UNSUPPORTED"},
		{"large body", `{"schema":"` + strings.Repeat("x", 1<<20) + `"}`, principal, 413, "PAYLOAD_TOO_LARGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture()
			mux := http.NewServeMux()
			RegisterRoutes(mux, NewHandler(f.service, func(context.Context) (contracts.Principal, bool) { return tc.user, tc.user.UserID != "" }))
			r := httptest.NewRequest("POST", "/api/v1/databases/"+testkit.DBSQLite+"/schema-diff", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatal(w.Code, w.Body.String())
			}
			assertReleased(t, f, 0)
		})
	}
}

func TestDiffHandlerReturnsSharedDTOAndSafeErrors(t *testing.T) {
	for _, failure := range []bool{false, true} {
		f := fixture()
		f.service.generators[database.SQLite] = generatorFunc(func(context.Context, Comparison) (Candidate, error) {
			if failure {
				return Candidate{}, atlasUnavailable("Atlas tidak tersedia.", nil)
			}
			return Candidate{SQL: "ALTER TABLE items ADD COLUMN status TEXT;", Executable: true}, nil
		})
		mux := http.NewServeMux()
		RegisterRoutes(mux, NewHandler(f.service, func(context.Context) (contracts.Principal, bool) { return principal, true }))
		r := httptest.NewRequest("POST", "/api/v1/databases/"+testkit.DBSQLite+"/schema-diff", strings.NewReader(`{"desired_database_id":"`+testkit.DBSQLiteDesired+`","schema":"main"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if failure {
			if w.Code != 503 || !strings.Contains(w.Body.String(), "DIFF_UNAVAILABLE") {
				t.Fatal(w.Code, w.Body.String())
			}
		} else {
			var body struct {
				Data DiffResult `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || !body.Data.Executable || body.Data.Driver != database.SQLite || body.Data.ExpectedVersion != 7 || len(body.Data.Changes) != 1 || body.Data.CandidateSQL == "" || body.Data.Warnings == nil {
				t.Fatal(w.Code, w.Body.String(), err)
			}
		}
		assertReleased(t, f, 2)
	}
}
