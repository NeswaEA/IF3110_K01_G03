package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/auth"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/connections"
	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

const userPrefix = "10000000-0000-4000-8000-00000000000"
const groupPrefix = "20000000-0000-4000-8000-00000000000"
const databasePrefix = "30000000-0000-4000-8000-00000000000"

type fixtureConnection struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Driver database.Driver `json:"driver"`
	Config json.RawMessage `json:"config"`
}

func seedFixtures(ctx context.Context, tx *sql.Tx) error {
	entries, err := readFixtureConnections(os.Getenv("FIXTURE_CONNECTIONS_FILE"))
	if err != nil {
		return err
	}
	cipher, err := connections.NewCipher(os.Getenv("CONNECTION_ENCRYPTION_KEY"))
	if err != nil {
		return errors.New("CONNECTION_ENCRYPTION_KEY must be base64 encoding of 32 bytes")
	}
	password := os.Getenv("FIXTURE_PASSWORD")
	if err := auth.ValidatePassword(password); err != nil {
		return errors.New("FIXTURE_PASSWORD must be 12-128 characters and at most 1024 bytes")
	}
	for _, entry := range entries {
		if err := insertFixtureConnection(ctx, tx, cipher, entry); err != nil {
			return err
		}
	}
	for i, name := range []string{"fixture-admin", "viewer", "editor", "outsider"} {
		hash, err := (auth.PasswordHasher{}).Hash(ctx, password)
		if err != nil {
			return errors.New("fixture password hashing failed")
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$2,$3,$4)
   ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash,is_platform_admin=excluded.is_platform_admin,active=true,updated_at=now()
   WHERE users.username=excluded.username`, userPrefix+string(rune('1'+i)), name, hash, i == 0)
		if err != nil {
			return errors.New("fixture user conflicts with an existing account")
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("fixture user ID is already in use")
		}
	}
	for i, name := range []string{"fixture-view", "fixture-edit"} {
		id := groupPrefix + string(rune('1'+i))
		result, err := tx.ExecContext(ctx, `INSERT INTO groups(id,name) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET name=excluded.name WHERE groups.name=excluded.name`, id, name)
		if err != nil {
			return errors.New("fixture group conflicts with an existing group")
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("fixture group ID is already in use")
		}
	}
	// Reset only fixture-owned memberships and grants, then invalidate affected sessions.
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id IN
  (SELECT user_id FROM group_memberships WHERE group_id IN ($1,$2)) OR user_id IN ($3,$4,$5,$6)`,
		groupPrefix+"1", groupPrefix+"2", userPrefix+"1", userPrefix+"2", userPrefix+"3", userPrefix+"4")
	if err != nil {
		return errors.New("could not revoke fixture sessions")
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM group_memberships WHERE group_id IN ($1,$2) OR user_id IN ($3,$4,$5,$6)`, groupPrefix+"1", groupPrefix+"2", userPrefix+"1", userPrefix+"2", userPrefix+"3", userPrefix+"4"); err != nil {
		return errors.New("could not reset fixture memberships")
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM group_database_access WHERE group_id IN ($1,$2)`, groupPrefix+"1", groupPrefix+"2"); err != nil {
		return errors.New("could not reset fixture grants")
	}
	for _, pair := range [][2]string{{"1", "2"}, {"1", "3"}, {"2", "3"}} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO group_memberships(group_id,user_id) VALUES($1,$2)`, groupPrefix+pair[0], userPrefix+pair[1]); err != nil {
			return errors.New("could not seed memberships")
		}
	}
	for _, group := range []string{"1", "2"} {
		for i := 1; i <= 4; i++ {
			level := "view"
			if group == "2" && i <= 2 {
				level = "edit"
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO group_database_access(group_id,database_id,level) VALUES($1,$2,$3)`, groupPrefix+group, databasePrefix+string(rune('0'+i)), level); err != nil {
				return errors.New("could not seed grants")
			}
		}
	}
	return nil
}

func readFixtureConnections(path string) ([]fixtureConnection, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("FIXTURE_CONNECTIONS_FILE must point to a readable JSON file")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, errors.New("fixture connections file exceeds 1 MiB or cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var entries []fixtureConnection
	if decoder.Decode(&entries) != nil || decoder.Decode(new(any)) != io.EOF || len(entries) != 4 {
		return nil, errors.New("fixture connections file must contain an array of four connection objects")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		expected := database.Postgres
		if entry.ID == databasePrefix+"2" || entry.ID == databasePrefix+"4" {
			expected = database.SQLite
		}
		validID := false
		for _, suffix := range []string{"1", "2", "3", "4"} {
			validID = validID || entry.ID == databasePrefix+suffix
		}
		if !validID || seen[entry.ID] || entry.Driver != expected || len(entry.Name) == 0 || len([]rune(entry.Name)) > 100 {
			return nil, errors.New("fixture connection IDs, names, or drivers are invalid")
		}
		if _, err := connections.ParseConfig(entry.Driver, entry.Config, connections.Policy{AllowInsecureSSL: true}); err != nil {
			return nil, errors.New("invalid fixture connection configuration")
		}
		seen[entry.ID] = true
	}
	return entries, nil
}

func insertFixtureConnection(ctx context.Context, tx *sql.Tx, cipher *connections.Cipher, entry fixtureConnection) error {
	var name string
	var driver database.Driver
	var encrypted []byte
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT name,driver,config_encrypted,active FROM database_connections WHERE id=$1 FOR UPDATE`, entry.ID).Scan(&name, &driver, &encrypted, &active)
	if err == nil {
		plain, err := cipher.Open(encrypted, []byte(entry.ID))
		if err != nil {
			return errors.New("could not decrypt existing fixture connection")
		}
		old, err := connections.ParseConfig(driver, plain, connections.Policy{AllowInsecureSSL: true})
		want, _ := connections.ParseConfig(entry.Driver, entry.Config, connections.Policy{AllowInsecureSSL: true})
		if err != nil || !active || name != entry.Name || !reflect.DeepEqual(old, want) {
			return errors.New("existing fixture connection differs; seed does not replace registered targets")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return errors.New("could not inspect fixture connection")
	}
	encrypted, err = cipher.Seal(entry.Config, []byte(entry.ID))
	if err != nil {
		return errors.New("could not encrypt fixture connection")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO database_connections(id,name,driver,config_encrypted) VALUES($1,$2,$3,$4)`, entry.ID, entry.Name, entry.Driver, encrypted)
	if err != nil {
		return errors.New("could not seed connection")
	}
	return nil
}
