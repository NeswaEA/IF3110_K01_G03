package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

type metadataMigration struct {
	version  string
	checksum string
	sql      string
}

var migrationFilename = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

func MigrateMetadata(ctx context.Context, db *sql.DB, files fs.FS) error {
	migrations, err := readMetadataMigrations(files)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("metadata migration transaction failed")
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x4d455441534348)); err != nil {
		return errors.New("metadata migration lock failed")
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_schema_migrations (
        version text PRIMARY KEY,
        checksum text NOT NULL,
        applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
    )`); err != nil {
		return errors.New("metadata migration ledger creation failed")
	}
	rows, err := tx.QueryContext(ctx, "SELECT version, checksum FROM app_schema_migrations ORDER BY version")
	if err != nil {
		return errors.New("metadata migration ledger read failed")
	}
	applied := make(map[string]string)
	head := ""
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return errors.New("metadata migration ledger read failed")
		}
		applied[version] = checksum
		head = version
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("metadata migration ledger read failed")
	}
	available := make(map[string]bool)
	for _, migration := range migrations {
		available[migration.version] = true
		if checksum, ok := applied[migration.version]; ok {
			if checksum != migration.checksum {
				return fmt.Errorf("migration %s checksum mismatch", migration.version)
			}
		} else if migration.version <= head {
			return fmt.Errorf("migration %s is older than the applied head", migration.version)
		}
	}
	for version := range applied {
		if !available[version] {
			return fmt.Errorf("applied migration %s is missing", version)
		}
	}
	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("migration %s execution failed", migration.version)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO app_schema_migrations (version, checksum) VALUES ($1, $2)", migration.version, migration.checksum); err != nil {
			return fmt.Errorf("migration %s recording failed", migration.version)
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("metadata migration commit was not confirmed; rerun to verify the ledger")
	}
	return nil
}

func readMetadataMigrations(files fs.FS) ([]metadataMigration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, errors.New("metadata migration files could not be read")
	}
	var migrations []metadataMigration
	versions := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationFilename.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, errors.New("metadata migration filename must use NNNN_name.sql format")
		}
		version := match[1]
		if versions[version] {
			return nil, fmt.Errorf("duplicate migration version %s", version)
		}
		versions[version] = true
		body, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("migration %s could not be read", version)
		}
		if strings.TrimSpace(string(body)) == "" {
			return nil, fmt.Errorf("migration %s is empty", version)
		}
		checksum := sha256.Sum256(body)
		migrations = append(migrations, metadataMigration{version: version, checksum: hex.EncodeToString(checksum[:]), sql: string(body)})
	}
	if len(migrations) == 0 {
		return nil, errors.New("no metadata migrations found")
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}
