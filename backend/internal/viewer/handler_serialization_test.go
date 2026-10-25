package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/databasetest"
	sqliteadapter "github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/sqlite"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestViewerSQLiteNumericResponses(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "values.sqlite"))+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE sample_values(id INTEGER PRIMARY KEY, value REAL)`); err != nil {
		t.Fatal(err)
	}
	provider := databasetest.NewProvider(database.TargetHandle{DatabaseID: testkit.DBSQLite, Driver: database.SQLite, DB: db})
	service := NewService(provider, sqliteadapter.Inspector{}, map[database.Driver]TableReader{database.SQLite: sqliteadapter.Reader{}}, time.Second)
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewHandler(service, testkit.NewFixtureAccessChecker(), func(context.Context) (contracts.Principal, bool) {
		return contracts.Principal{UserID: testkit.UserView}, true
	}))
	for _, tc := range []struct {
		name, value string
		status      int
	}{
		{"finite", "1.25", 200},
		{"positive infinity", "1e999", 500},
		{"negative infinity", "-1e999", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM sample_values; INSERT INTO sample_values VALUES(1,` + tc.value + `)`); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			platform.RequestID(mux).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/databases/"+testkit.DBSQLite+"/data?schema=main&table=sample_values", nil))
			var body struct {
				Data  database.TableData `json:"data"`
				Page  *platform.Page     `json:"page"`
				Error struct {
					Code      string `json:"code"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				if len(body.Data.Rows) != 1 || body.Data.Rows[0][1] != float64(1.25) || body.Page == nil || body.Page.HasMore {
					t.Fatal("finite data or pagination changed", w.Body.String())
				}
			} else if body.Error.Code != "INTERNAL_ERROR" || body.Error.RequestID == "" || body.Error.RequestID != w.Header().Get("X-Request-ID") {
				t.Fatal("nonfinite data must produce a correlated internal error", w.Body.String())
			}
			if provider.Acquired() != provider.Released() || db.Stats().InUse != 0 {
				t.Fatal("viewer leaked its target lease or connection")
			}
		})
	}
}
