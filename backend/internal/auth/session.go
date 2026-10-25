package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

type SessionOptions struct {
	IdleTimeout      time.Duration
	AbsoluteTimeout  time.Duration
	RotationInterval time.Duration
	GracePeriod      time.Duration
}

func DefaultSessionOptions() SessionOptions {
	return SessionOptions{
		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 8 * time.Hour,
		RotationInterval: 15 * time.Minute, GracePeriod: 30 * time.Second,
	}
}

type Session struct {
	ID                string              `json:"-"`
	Principal         contracts.Principal `json:"-"`
	Token             string              `json:"-"`
	CSRFToken         string              `json:"-"`
	AbsoluteExpiresAt time.Time           `json:"-"`
}

type SessionStore struct {
	db      *sql.DB
	clock   contracts.Clock
	options SessionOptions
}

var _ contracts.SessionInvalidator = (*SessionStore)(nil)

func NewSessionStore(db *sql.DB, clock contracts.Clock, options SessionOptions) (*SessionStore, error) {
	if db == nil || clock == nil {
		return nil, errors.New("session store requires a database and clock")
	}
	for _, duration := range []time.Duration{options.IdleTimeout, options.AbsoluteTimeout, options.RotationInterval, options.GracePeriod} {
		if duration < time.Microsecond || duration%time.Microsecond != 0 {
			return nil, errors.New("session durations must be positive whole microseconds")
		}
	}
	return &SessionStore{db: db, clock: clock, options: options}, nil
}

func (s *SessionStore) CreateInTx(ctx context.Context, tx *sql.Tx, userID string) (Session, error) {
	if tx == nil {
		return Session{}, sessionFailure(errors.New("session creation requires a transaction"))
	}
	var principal contracts.Principal
	if userID != "" {
		err := tx.QueryRowContext(ctx, `SELECT id, username, is_platform_admin FROM users WHERE id=$1 AND active FOR SHARE`, userID).
			Scan(&principal.UserID, &principal.Username, &principal.IsPlatformAdmin)
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, unauthenticated()
		}
		if err != nil {
			return Session{}, sessionFailure(err)
		}
	}
	now := s.now()
	lifetime := s.options.AbsoluteTimeout
	if userID == "" {
		lifetime = 10 * time.Minute
	}
	id := make([]byte, 16)
	csrf := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return Session{}, sessionFailure(err)
	}
	if _, err := rand.Read(csrf); err != nil {
		return Session{}, sessionFailure(err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	result := Session{
		ID:        fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]),
		Principal: principal, CSRFToken: base64.RawURLEncoding.EncodeToString(csrf),
		AbsoluteExpiresAt: now.Add(lifetime),
	}
	if userID != "" {
		result.Principal.SessionID = result.ID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions
		(id, user_id, created_at, absolute_expires_at, last_seen_at, rotated_at, csrf_secret)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $3, $3, $5)`,
		result.ID, userID, now, result.AbsoluteExpiresAt, csrf)
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	result.Token, err = insertSessionToken(ctx, tx, result.ID, 1, now, result.AbsoluteExpiresAt)
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	return result, nil
}

func (s *SessionStore) Resolve(ctx context.Context, token string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, sessionFailure(err)
	}
	hash, ok := sessionTokenHash(token)
	if !ok {
		return Session{}, unauthenticated()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	defer tx.Rollback()
	var result Session
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM session_tokens WHERE token_hash=$1`, hash).Scan(&result.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	var userID sql.NullString
	var revoked sql.NullTime
	var lastSeen, rotated time.Time
	var generation int
	var rotationRequired bool
	var csrf []byte
	err = tx.QueryRowContext(ctx, `SELECT user_id, absolute_expires_at, last_seen_at, rotated_at,
		generation, rotation_required, revoked_at, csrf_secret FROM sessions WHERE id=$1 FOR UPDATE`, result.ID).
		Scan(&userID, &result.AbsoluteExpiresAt, &lastSeen, &rotated, &generation, &rotationRequired, &revoked, &csrf)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	var current bool
	var tokenGeneration int
	var validUntil time.Time
	// Re-read after the session lock: another request may have rotated this token.
	err = tx.QueryRowContext(ctx, `SELECT is_current, generation, valid_until FROM session_tokens
		WHERE token_hash=$1 AND session_id=$2`, hash, result.ID).Scan(&current, &tokenGeneration, &validUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	now := s.now()
	if now.Before(lastSeen) {
		now = lastSeen
	}
	if revoked.Valid || !now.Before(result.AbsoluteExpiresAt) || now.Sub(lastSeen) >= s.options.IdleTimeout ||
		!now.Before(validUntil) || tokenGeneration > generation || (current && tokenGeneration != generation) ||
		(rotationRequired && !current) {
		return Session{}, unauthenticated()
	}
	if userID.Valid {
		err = tx.QueryRowContext(ctx, `SELECT id, username, is_platform_admin FROM users WHERE id=$1 AND active`, userID.String).
			Scan(&result.Principal.UserID, &result.Principal.Username, &result.Principal.IsPlatformAdmin)
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, unauthenticated()
		}
		if err != nil {
			return Session{}, sessionFailure(err)
		}
		result.Principal.SessionID = result.ID
	}
	if current && (rotationRequired || now.Sub(rotated) >= s.options.RotationInterval) {
		graceUntil := now.Add(s.options.GracePeriod)
		if graceUntil.After(result.AbsoluteExpiresAt) {
			graceUntil = result.AbsoluteExpiresAt
		}
		_, err = tx.ExecContext(ctx, `UPDATE session_tokens SET is_current=false, valid_until=$2 WHERE token_hash=$1`, hash, graceUntil)
		if err != nil {
			return Session{}, sessionFailure(err)
		}
		generation++
		result.Token, err = insertSessionToken(ctx, tx, result.ID, generation, now, result.AbsoluteExpiresAt)
		if err != nil {
			return Session{}, sessionFailure(err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE sessions SET generation=$2, rotated_at=$3, rotation_required=false WHERE id=$1`, result.ID, generation, now)
		if err != nil {
			return Session{}, sessionFailure(err)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at=$2 WHERE id=$1`, result.ID, now)
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, sessionFailure(err)
	}
	result.CSRFToken = base64.RawURLEncoding.EncodeToString(csrf)
	return result, nil
}

func (s *SessionStore) RevokeInTx(ctx context.Context, tx *sql.Tx, sessionID string) error {
	if tx == nil {
		return sessionFailure(errors.New("session revocation requires a transaction"))
	}
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, sessionID, s.now())
	if err != nil {
		return sessionFailure(err)
	}
	return nil
}

func (s *SessionStore) RevokeUsers(ctx context.Context, tx *sql.Tx, userIDs []string) error {
	if tx == nil {
		return sessionFailure(errors.New("session revocation requires a transaction"))
	}
	if len(userIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$2 WHERE id IN
		(SELECT id FROM sessions WHERE user_id=ANY($1::uuid[]) AND revoked_at IS NULL ORDER BY id FOR UPDATE)`, userIDs, s.now())
	if err != nil {
		return sessionFailure(err)
	}
	return nil
}

func (s *SessionStore) RequireRotation(ctx context.Context, tx *sql.Tx, userIDs []string) error {
	if tx == nil {
		return sessionFailure(errors.New("session rotation requires a transaction"))
	}
	if len(userIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET rotation_required=true WHERE id IN
		(SELECT id FROM sessions WHERE user_id=ANY($1::uuid[]) AND revoked_at IS NULL ORDER BY id FOR UPDATE)`, userIDs)
	if err != nil {
		return sessionFailure(err)
	}
	return nil
}

// pruneInTx deletes expired sessions (their tokens cascade). It runs where
// anonymous sessions are created, so unauthenticated traffic cannot grow the table
// without bound. SKIP LOCKED leaves rows in use to a later call instead of waiting.
// shortcut: no index on absolute_expires_at, so each call scans sessions; add one if live sessions reach ~100k.
func (s *SessionStore) pruneInTx(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return sessionFailure(errors.New("session pruning requires a transaction"))
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id IN
		(SELECT id FROM sessions WHERE absolute_expires_at <= $1 ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED)`, s.now())
	if err != nil {
		return sessionFailure(err)
	}
	return nil
}

func (s *SessionStore) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

func insertSessionToken(ctx context.Context, tx *sql.Tx, sessionID string, generation int, now, deadline time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	_, err := tx.ExecContext(ctx, `INSERT INTO session_tokens (token_hash, session_id, generation, is_current, valid_until, created_at)
		VALUES ($1, $2, $3, true, $4, $5)`, hash[:], sessionID, generation, deadline, now)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func sessionTokenHash(token string) ([]byte, bool) {
	if len(token) != 43 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	hash := sha256.Sum256(raw)
	return hash[:], true
}

func unauthenticated() error {
	return &contracts.DomainError{Code: "UNAUTHENTICATED", Message: "Silakan login untuk melanjutkan."}
}

func sessionFailure(err error) error {
	return &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Session tidak dapat diproses.", Cause: err}
}
