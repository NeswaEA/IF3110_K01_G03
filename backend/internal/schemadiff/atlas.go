package schemadiff

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

type AtlasGenerator struct {
	Config  database.DiffConfigProvider
	Binary  string
	Timeout time.Duration
}

var _ Generator = AtlasGenerator{}

func (g AtlasGenerator) Generate(ctx context.Context, c Comparison) (Candidate, error) {
	if c.Source.Driver != database.Postgres || c.Desired.Driver != database.Postgres || c.Schema == "" || database.Reserved(c.Schema, "") || !utf8.ValidString(c.Schema) || strings.ContainsRune(c.Schema, 0) {
		return Candidate{}, database.ErrDiffUnsupported
	}
	if g.Config == nil {
		return Candidate{}, atlasUnavailable("Konfigurasi Atlas belum tersedia.", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	dir, err := os.MkdirTemp("", "if3110-atlas-home-")
	if err != nil {
		return Candidate{}, atlasUnavailable("Direktori sementara Atlas tidak tersedia.", err)
	}
	defer os.RemoveAll(dir)
	if err := g.check(ctx, dir); err != nil {
		return Candidate{}, err
	}
	var candidate Candidate
	err = g.Config.WithDiffConfig(ctx, c.Source.DatabaseID, c.Desired.DatabaseID, func(path string) error {
		info, err := os.Stat(path)
		if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return atlasUnavailable("Config sementara Atlas tidak valid.", err)
		}
		configURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
		var schemas strings.Builder
		writer := csv.NewWriter(&schemas)
		if err := writer.Write([]string{c.Schema}); err != nil {
			return err
		}
		writer.Flush()
		out, err := g.run(ctx, dir, "schema", "diff", "--config", configURL, "--env", "source", "--from", "env://url", "--to", "env://schema.src", "--schema", strings.TrimSuffix(schemas.String(), "\n"), "--format", "{{ sql . }}")
		if err != nil {
			return err
		}
		if !utf8.ValidString(out) || strings.ContainsRune(out, 0) || len(out) > 256<<10 {
			return atlasUnavailable("Kandidat Atlas melebihi batas SQL atau memiliki encoding tidak valid.", nil)
		}
		if strings.TrimSpace(out) == "" {
			if len(c.Changes) > 0 {
				return atlasUnavailable("Atlas tidak menghasilkan kandidat untuk perubahan schema yang terdeteksi.", nil)
			}
			candidate = Candidate{Executable: true}
			return nil
		}
		if !atlasTransactionalDDL(out, c.Schema) {
			candidate = Candidate{Warnings: []string{"Keluaran Atlas memuat operasi di luar DDL transaksional yang didukung; buat migrasi manual."}}
			return nil
		}
		candidate = Candidate{SQL: out, Executable: true}
		if len(c.Changes) == 0 {
			candidate.Changes = []DiffChange{{Kind: Other, Object: c.Schema, Destructive: true}}
			candidate.Warnings = []string{"Atlas mendeteksi perubahan schema di luar detail snapshot bersama; tinjau kandidat SQL."}
		}
		return nil
	})
	if err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func (g AtlasGenerator) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	dir, err := os.MkdirTemp("", "if3110-atlas-home-")
	if err != nil {
		return atlasUnavailable("Direktori sementara Atlas tidak tersedia.", err)
	}
	defer os.RemoveAll(dir)
	return g.check(ctx, dir)
}

func (g AtlasGenerator) check(ctx context.Context, home string) error {
	out, err := g.run(ctx, home, "version")
	if err != nil {
		return err
	}
	line, _, _ := strings.Cut(out, "\n")
	fields := strings.Fields(line)
	if len(fields) != 5 || fields[0] != "atlas" || fields[1] != "community" || fields[2] != "community" || fields[3] != "version" || fields[4] != "v1.3.0" {
		return atlasUnavailable("Atlas harus memakai Community v1.3.0.", nil)
	}
	return nil
}

func (g AtlasGenerator) timeout() time.Duration {
	if g.Timeout <= 0 {
		return 30 * time.Second
	}
	return g.Timeout
}

type atlasOutput struct {
	bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func (b *atlasOutput) Write(p []byte) (int, error) {
	if len(p) > 1<<20-b.Len() {
		b.exceeded = true
		b.cancel()
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}

func (g AtlasGenerator) run(ctx context.Context, home string, args ...string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bin := g.Binary
	if bin == "" {
		bin = "atlas"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "ATLAS_NO_UPDATE_NOTIFIER=1", "ATLAS_NO_UPGRADE_SUGGESTIONS=1", "NO_COLOR=1", "LANG=C.UTF-8", "TZ=UTC"}
	cmd.Dir = home
	cmd.WaitDelay = 250 * time.Millisecond
	out, stderr := atlasOutput{cancel: cancel}, atlasOutput{cancel: cancel}
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if out.exceeded || stderr.exceeded {
		return "", atlasUnavailable("Keluaran proses Atlas melebihi batas 1 MiB.", nil)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", atlasUnavailable("Batas waktu proses Atlas terlampaui.", ctx.Err())
		}
		return "", atlasUnavailable("Atlas tidak tersedia atau gagal menghasilkan kandidat SQL.", err)
	}
	return out.String(), nil
}

func atlasUnavailable(message string, cause error) error {
	return &contracts.DomainError{Code: "DIFF_UNAVAILABLE", Message: message, Cause: cause}
}
