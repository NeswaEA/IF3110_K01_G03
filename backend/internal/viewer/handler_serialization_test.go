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
		name, value, want string
	}{
		{"finite", "1.25", "1.25"},
		{"positive infinity", "1e999", "Infinity"},
		{"negative infinity", "-1e999", "-Infinity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM sample_values; INSERT INTO sample_values VALUES(1,` + tc.value + `)`); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			platform.RequestID(mux).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/databases/"+testkit.DBSQLite+"/data?schema=main&table=sample_values", nil))
			var body struct {
				Data database.TableData `json:"data"`
				Page *platform.Page     `json:"page"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
			if len(body.Data.Rows) != 1 || body.Data.Rows[0][1] != tc.want || body.Data.Columns[1].Encoding != database.EncDecimalString || body.Page == nil || body.Page.HasMore {
				t.Fatal("REAL value must keep its meaning as a decimal string", w.Body.String())
			}
			if provider.Acquired() != provider.Released() || db.Stats().InUse != 0 {
				t.Fatal("viewer leaked its target lease or connection")
			}
		})
	}
}
