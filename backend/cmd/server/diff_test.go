package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

func TestDiffGeneratorCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		available    bool
	}{
		{"available", "#!/bin/sh\nprintf 'atlas community community version v1.3.0\\n'\n", true},
		{"wrong version", "#!/bin/sh\nprintf 'atlas community community version v1.4.0\\n'\n", false},
		{"missing", "", false},
		{"timeout", "#!/bin/sh\nexec sleep 60\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "atlas")
			if tc.script != "" {
				if err := os.WriteFile(bin, []byte(tc.script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			timeout := 2 * time.Second
			if tc.name == "timeout" {
				timeout = 100 * time.Millisecond
			}
			generators, features := diffGenerators(t.Context(), nil, runtimeConfig{AtlasBin: bin, DiffTimeout: timeout})
			if features[database.Postgres] != tc.available || !features[database.SQLite] || generators[database.SQLite] == nil || generators[database.Postgres] == nil {
				t.Fatal("invalid generator capabilities", features)
			}
		})
	}
}
