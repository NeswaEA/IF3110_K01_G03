//go:build atlas

package connections

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database/postgres"
)

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

func TestAtlasConfigSmoke(t *testing.T) {
	bin := requireEnv(t, "ATLAS_BIN")
	e := newEnv(t, nil)
	src, err := e.svc.Register(ctx, RegisterInput{Name: "source", Driver: database.Postgres, Config: targetConfig(t, nil)})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := e.svc.Register(ctx, RegisterInput{Name: "desired", Driver: database.Postgres, Config: targetConfig(t, func(m map[string]any) { m["database"] = "demo_desired" })})
	if err != nil {
		t.Fatal(err)
	}
	from, release, err := e.reg.Acquire(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	to, release, err := e.reg.Acquire(ctx, dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, h := range []database.TargetHandle{from, to} {
		if _, err := h.DB.Exec(`SET ROLE platform_editor; CREATE TABLE public.atlas_config_probe(id INTEGER PRIMARY KEY); RESET ROLE`); err != nil {
			t.Fatal("cannot create Atlas fixture")
		}
		t.Cleanup(func() {
			_, _ = h.DB.Exec(`SET ROLE platform_editor; DROP TABLE IF EXISTS public.atlas_config_probe; RESET ROLE`)
		})
	}
	if _, err := to.DB.Exec(`SET ROLE platform_editor; ALTER TABLE public.atlas_config_probe ADD COLUMN desired_only TEXT; RESET ROLE`); err != nil {
		t.Fatal("cannot alter desired fixture")
	}
	before, err := (postgres.Inspector{}).Inspect(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	childEnv := []string{"HOME=" + t.TempDir(), "ATLAS_NO_UPDATE_NOTIFIER=1", "ATLAS_NO_UPGRADE_SUGGESTIONS=1", "NO_COLOR=1"}
	run := func(c context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(c, bin, args...)
		cmd.Env = childEnv
		var out, stderr cappedOutput
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			return "", err
		}
		return out.String(), nil
	}
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	version, err := run(deadline, "version")
	if err != nil || !strings.Contains(version, "v1.3.0") || !strings.Contains(version, "community") {
		t.Fatal("ATLAS_BIN must be Community v1.3.0")
	}
	var seen string
	err = e.reg.WithDiffConfig(deadline, src.ID, dst.ID, func(path string) error {
		seen = path
		candidate, err := run(deadline, "schema", "diff", "--config", "file://"+path, "--env", "source", "--from", "env://url", "--to", "env://schema.src", "--schema", "public")
		if err != nil {
			return err
		}
		if !strings.Contains(candidate, "desired_only") {
			t.Error("Atlas did not generate desired column")
		}
		if strings.Contains(candidate, "_platform") {
			t.Error("reserved namespace leaked into diff")
		}
		return nil
	})
	if err != nil {
		t.Fatal("Atlas config smoke failed:", err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatal("temporary config remained")
	}
	after, err := (postgres.Inspector{}).Inspect(ctx, from)
	if err != nil || before.Fingerprint != after.Fingerprint {
		t.Fatal("generation changed source schema")
	}
}
