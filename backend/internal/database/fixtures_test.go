package database_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

const fixtureDir = "../../../tests/fixtures/api"

func decodeStrict(t *testing.T, file string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, file))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // a renamed or extra field in the fixture fails the Go DTO
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

func TestSchemaFixturesMatchDTOAndFingerprint(t *testing.T) {
	for _, file := range []string{"viewer_schema_populated.json", "viewer_schema_empty.json"} {
		t.Run(file, func(t *testing.T) {
			var env struct {
				Data database.SchemaSnapshot `json:"data"`
			}
			decodeStrict(t, file, &env)

			want, err := database.Fingerprint(env.Data)
			if err != nil {
				t.Fatal(err)
			}
			if env.Data.Fingerprint != want {
				t.Fatalf("fixture fingerprint = %s, Fingerprint() = %s", env.Data.Fingerprint, want)
			}
			if env.Data.Schemas == nil {
				t.Fatal("schemas must be [] not null")
			}
		})
	}
}

func TestDataFixturesMatchDTO(t *testing.T) {
	for _, file := range []string{"viewer_data_populated.json", "viewer_data_empty.json", "viewer_data_unordered.json"} {
		t.Run(file, func(t *testing.T) {
			var env struct {
				Data database.TableData `json:"data"`
				Page struct {
					Limit   int  `json:"limit"`
					Offset  int  `json:"offset"`
					HasMore bool `json:"has_more"`
				} `json:"page"`
			}
			decodeStrict(t, file, &env)
			if env.Data.Rows == nil {
				t.Fatal("rows must be [] not null")
			}
			for i, row := range env.Data.Rows {
				if len(row) != len(env.Data.Columns) {
					t.Fatalf("row %d has %d values for %d columns", i, len(row), len(env.Data.Columns))
				}
			}
		})
	}
}
