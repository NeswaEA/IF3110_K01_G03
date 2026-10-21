// Package connections is the registry of target databases: encrypted config,
// revisioned lifecycle, and the TargetProvider/DiffConfigProvider that hand out
// pooled leases. The registry core imports database only; handler.go maps
// its errors to DomainError.
package connections

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/database"
)

var (
	ErrInvalid          = errors.New("connections: invalid request")
	ErrRevisionConflict = errors.New("connections: revision conflict")
	ErrDuplicateTarget  = errors.New("connections: duplicate target")
	ErrIdentityLocked   = errors.New("connections: target identity cannot change after history exists")
)

// ValidationError carries per-field messages for INVALID_REQUEST.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string        { return "connections: invalid request" }
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

func invalid(field, msg string) *ValidationError {
	return &ValidationError{Fields: map[string]string{field: msg}}
}

type PostgresConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Database   string `json:"database"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	SSLMode    string `json:"sslmode"`
	EditorRole string `json:"editor_role"`
}

type SQLiteConfig struct {
	File string `json:"file"`
}

// Config is the plaintext form. It only lives in memory; never log or serialise it publicly.
type Config struct {
	Driver   database.Driver
	Postgres *PostgresConfig
	SQLite   *SQLiteConfig
}

// Validation limits that depend on the environment.
type Policy struct {
	// AllowInsecureSSL permits sslmode=disable (local demo networks only).
	AllowInsecureSSL bool
}

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,253}$`)
	roleRe   = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	sqliteRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.(sqlite|db)$`)
	sslModes = map[string]bool{"disable": true, "require": true, "verify-ca": true, "verify-full": true}
	badRoles = map[string]bool{"postgres": true, "public": true, "none": true, "current_user": true, "session_user": true}
)

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

// ParseConfig decodes and validates a config object sent by an admin.
// Unknown fields are rejected so typos are visible.
func ParseConfig(driver database.Driver, raw json.RawMessage, p Policy) (Config, error) {
	switch driver {
	case database.Postgres:
		var c PostgresConfig
		if err := decodeStrict(raw, &c); err != nil {
			return Config{}, invalid("config", "objek konfigurasi tidak valid")
		}
		if err := validatePostgres(c, p); err != nil {
			return Config{}, err
		}
		return Config{Driver: driver, Postgres: &c}, nil
	case database.SQLite:
		var c SQLiteConfig
		if err := decodeStrict(raw, &c); err != nil {
			return Config{}, invalid("config", "objek konfigurasi tidak valid")
		}
		if err := validateSQLite(c); err != nil {
			return Config{}, err
		}
		return Config{Driver: driver, SQLite: &c}, nil
	default:
		return Config{}, invalid("driver", "driver harus postgres atau sqlite")
	}
}

func validatePostgres(c PostgresConfig, p Policy) *ValidationError {
	f := map[string]string{}
	if !hostRe.MatchString(c.Host) {
		f["config.host"] = "host tidak valid"
	}
	if c.Port < 1 || c.Port > 65535 {
		f["config.port"] = "harus antara 1 dan 65535"
	}
	if c.Database == "" || len(c.Database) > 63 || strings.ContainsAny(c.Database, "\x00\r\n") {
		f["config.database"] = "nama database tidak valid"
	}
	if c.Username == "" || len(c.Username) > 63 || strings.ContainsAny(c.Username, "\x00\r\n") {
		f["config.username"] = "username tidak valid"
	}
	if c.Password == "" || len(c.Password) > 256 {
		f["config.password"] = "password wajib diisi"
	}
	switch {
	case !sslModes[c.SSLMode]:
		f["config.sslmode"] = "harus disable, require, verify-ca, atau verify-full"
	case c.SSLMode == "disable" && !p.AllowInsecureSSL:
		f["config.sslmode"] = "sslmode=disable hanya untuk lingkungan lokal"
	}
	if !roleRe.MatchString(c.EditorRole) || badRoles[c.EditorRole] || strings.HasPrefix(c.EditorRole, "pg_") {
		f["config.editor_role"] = "harus nama role terbatas yang valid"
	}
	if len(f) > 0 {
		return &ValidationError{Fields: f}
	}
	return nil
}

func validateSQLite(c SQLiteConfig) *ValidationError {
	if !sqliteRe.MatchString(c.File) {
		return invalid("config.file", "harus nama berkas .sqlite atau .db tanpa direktori")
	}
	return nil
}

func (c PostgresConfig) DSN(connectTimeoutSeconds int) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.Username, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Database,
		RawQuery: url.Values{
			"sslmode":          {c.SSLMode},
			"connect_timeout":  {strconv.Itoa(connectTimeoutSeconds)},
			"application_name": {"if3110-platform"},
		}.Encode(),
	}
	return u.String()
}

func encodeConfig(c Config) ([]byte, error) {
	switch c.Driver {
	case database.Postgres:
		return json.Marshal(c.Postgres)
	case database.SQLite:
		return json.Marshal(c.SQLite)
	}
	return nil, fmt.Errorf("connections: unknown driver %q", c.Driver)
}

func decodeConfig(driver database.Driver, plain []byte) (Config, error) {
	c := Config{Driver: driver}
	switch driver {
	case database.Postgres:
		c.Postgres = new(PostgresConfig)
		return c, decodeStrict(plain, c.Postgres)
	case database.SQLite:
		c.SQLite = new(SQLiteConfig)
		return c, decodeStrict(plain, c.SQLite)
	}
	return c, fmt.Errorf("connections: unknown driver %q", driver)
}

// PublicPostgresConfig and PublicSQLiteConfig are the only config shapes that
// may leave the backend (no password, ciphertext, or DSN).
type PublicPostgresConfig struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Database       string `json:"database"`
	Username       string `json:"username"`
	SSLMode        string `json:"sslmode"`
	EditorRole     string `json:"editor_role"`
	HasCredentials bool   `json:"has_credentials"`
}

type PublicSQLiteConfig struct {
	File string `json:"file"`
}

func (c Config) Public() any {
	if c.Driver == database.SQLite {
		return PublicSQLiteConfig{File: c.SQLite.File}
	}
	p := c.Postgres
	return PublicPostgresConfig{Host: p.Host, Port: p.Port, Database: p.Database, Username: p.Username,
		SSLMode: p.SSLMode, EditorRole: p.EditorRole, HasCredentials: p.Password != ""}
}

// TargetIdentity names the physical target for duplicate detection. Remote host
// aliases that resolve to the same server are not detected.
func (c Config) TargetIdentity(sqliteDir string) (string, error) {
	if c.Driver == database.SQLite {
		p, err := ResolveSQLitePath(sqliteDir, c.SQLite.File)
		if err != nil {
			return "", err
		}
		return "sqlite:" + p, nil
	}
	p := c.Postgres
	return fmt.Sprintf("postgres:%s:%d/%s", strings.ToLower(p.Host), p.Port, p.Database), nil
}
