//go:build atlas

package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/platform"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/schemadiff"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/testkit"
)

func TestPostgresSchemaDiffHTTP(t *testing.T) {
	const groupID = "20000000-0000-4000-8000-000000000001"
	for _, available := range []bool{true, false} {
		name := "atlas available"
		if !available {
			name = "atlas missing"
		}
		t.Run(name, func(t *testing.T) {
			db, dsn := appDB(t)
			u, err := url.Parse(os.Getenv("TEST_TARGET_DATABASE_URL"))
			if err != nil || u.Host == "" {
				t.Fatal("TEST_TARGET_DATABASE_URL required")
			}
			key := base64.StdEncoding.EncodeToString(make([]byte, 32))
			cipher, err := connections.NewCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			password, _ := u.User.Password()
			port, _ := strconv.Atoi(u.Port())
			for i, id := range []string{testkit.DBPG, testkit.DBPGDesired} {
				databaseName := strings.TrimPrefix(u.Path, "/")
				if i == 1 {
					databaseName = "demo_desired"
				}
				plain, _ := json.Marshal(map[string]any{"host": u.Hostname(), "port": port, "database": databaseName, "username": u.User.Username(), "password": password, "sslmode": "disable", "editor_role": "platform_editor"})
				sealed, err := cipher.Seal(plain, []byte(id))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,$2,'postgres',$3)`, id, databaseName, sealed); err != nil {
					t.Fatal(err)
				}
			}
			for i, user := range []string{testkit.UserAdmin, testkit.UserEdit} {
				if _, err := db.Exec(`INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,'$argon2id$test',$3)`, user, []string{"admin", "editor"}[i], i == 0); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO groups(id,name) VALUES($1,'editor')`, groupID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO group_memberships(group_id,user_id) VALUES($1,$2)`, groupID, testkit.UserEdit); err != nil {
				t.Fatal(err)
			}
			for i, id := range []string{testkit.DBPG, testkit.DBPGDesired} {
				level := "edit"
				if i == 1 {
					level = "view"
				}
				if _, err := db.Exec(`INSERT INTO group_database_access(group_id,database_id,level) VALUES($1,$2,$3)`, groupID, id, level); err != nil {
					t.Fatal(err)
				}
			}
			store, err := auth.NewSessionStore(db, platform.SystemClock{}, auth.DefaultSessionOptions())
			if err != nil {
				t.Fatal(err)
			}
			newSession := func(user string) auth.Session {
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				session, err := store.CreateInTx(t.Context(), tx, user)
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				return session
			}
			admin, editor := newSession(testkit.UserAdmin), newSession(testkit.UserEdit)
			cfg := platform.Config{AppEnv: "test", MetadataDatabaseURL: dsn, AppOrigin: "http://localhost:8080", SessionIdleTimeout: 30 * time.Minute, SessionAbsoluteTimeout: 8 * time.Hour, SessionRotationInterval: 15 * time.Minute, SessionGracePeriod: 30 * time.Second}
			bin := os.Getenv("ATLAS_BIN")
			if bin == "" {
				t.Fatal("ATLAS_BIN required")
			}
			if !available {
				bin = filepath.Join(t.TempDir(), "absent")
			}
			h, cleanup, err := newApp(t.Context(), db, cfg, runtimeConfig{EncryptionKey: key, SQLiteDir: t.TempDir(), WebDir: testWeb(t), AtlasBin: bin, DiffTimeout: 30 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			request := func(method, path, body string, session auth.Session) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Origin", cfg.AppOrigin)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-CSRF-Token", session.CSRFToken)
				r.AddCookie(&http.Cookie{Name: "session", Value: session.Token})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			w := request("GET", "/api/v1/databases", "", editor)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"schema_diff":`+strconv.FormatBool(available)) {
				t.Fatal("wrong PostgreSQL capability", w.Code, w.Body.String())
			}
			base := "/api/v1/databases/" + testkit.DBPG
			body := `{"desired_database_id":"` + testkit.DBPGDesired + `","schema":"public"}`
			w = request("POST", base+"/schema-diff", body, editor)
			if w.Code != 503 || !strings.Contains(w.Body.String(), "TARGET_NOT_READY") {
				t.Fatal("diff bypassed readiness", w.Code, w.Body.String())
			}
			w = request("POST", "/api/v1/admin/connections/"+testkit.DBPG+"/test", `{}`, admin)
			if w.Code != 200 {
				t.Fatal("admin connection test", w.Code, w.Body.String())
			}
			w = request("POST", base+"/schema-diff", body, editor)
			if available {
				var response struct {
					Data schemadiff.DiffResult `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || !response.Data.Executable || response.Data.CandidateSQL == "" {
					t.Fatal("PostgreSQL diff endpoint", w.Code, w.Body.String(), err)
				}
			} else if w.Code != 503 || !strings.Contains(w.Body.String(), "DIFF_UNAVAILABLE") {
				t.Fatal("missing Atlas not reported", w.Code, w.Body.String())
			}
			w = request("GET", base+"/migration-head", "", editor)
			if w.Code != 200 {
				t.Fatal("Atlas failure broke manual migration head", w.Code, w.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM migration_drafts)+(SELECT count(*) FROM migration_runs)`).Scan(&count); err != nil || count != 0 {
				t.Fatal("diff created metadata", count, err)
			}
		})
	}
}
