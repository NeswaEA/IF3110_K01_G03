package connections

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveSQLitePath returns the absolute path of file inside dir (SQLITE_TARGET_DIR).
// It rejects directories in the name, traversal, absolute names, and any file
// whose real location (after symlinks) is outside dir. A missing file is
// allowed here; opening it later reports the target as unavailable.
func ResolveSQLitePath(dir, file string) (string, error) {
	if !sqliteRe.MatchString(file) || filepath.Base(file) != file {
		return "", invalid("config.file", "nama berkas tidak valid")
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return "", invalid("config.file", "direktori target SQLite belum dikonfigurasi")
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", invalid("config.file", "direktori target SQLite tidak dapat diakses")
	}
	p := filepath.Join(realDir, file)
	resolved, err := filepath.EvalSymlinks(p)
	switch {
	case err == nil:
		if !within(realDir, resolved) {
			return "", invalid("config.file", "berkas berada di luar direktori target")
		}
		return resolved, nil
	case os.IsNotExist(err):
		// A dangling symlink also lands here; Lstat tells it apart from a plain missing file.
		if _, lerr := os.Lstat(p); lerr == nil {
			return "", invalid("config.file", "berkas berada di luar direktori target")
		}
		return p, nil
	default:
		return "", invalid("config.file", "berkas tidak dapat diperiksa")
	}
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
