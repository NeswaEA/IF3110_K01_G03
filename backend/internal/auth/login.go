package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Unknown users still incur the same password verification cost.
const dummyPasswordHash = passwordHashPrefix + "AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (s *SessionStore) login(ctx context.Context, prelogin Session, username, password string) (Session, error) {
	if prelogin.ID == "" || prelogin.Principal.UserID != "" {
		return Session{}, unauthenticated()
	}
	normalized, _ := NormalizeUsername(username)
	var userID, passwordHash string
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash, active FROM users WHERE username=$1`, normalized).
		Scan(&userID, &passwordHash, &active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Session{}, sessionFailure(err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		passwordHash = dummyPasswordHash
	}
	match, err := (PasswordHasher{}).Verify(ctx, password, passwordHash)
	if err != nil {
		return Session{}, err
	}
	if !match || userID == "" || !active {
		return Session{}, unauthenticated()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	defer tx.Rollback()
	var currentHash string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=$1 AND active FOR SHARE`, userID).Scan(&currentHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && currentHash != passwordHash) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	var deadline, lastSeen time.Time
	var revoked sql.NullTime
	var owner sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT user_id, absolute_expires_at, last_seen_at, revoked_at
		FROM sessions WHERE id=$1 FOR UPDATE`, prelogin.ID).Scan(&owner, &deadline, &lastSeen, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, unauthenticated()
	}
	if err != nil {
		return Session{}, sessionFailure(err)
	}
	now := s.now()
	if owner.Valid || revoked.Valid || !now.Before(deadline) || now.Sub(lastSeen) >= s.options.IdleTimeout {
		return Session{}, unauthenticated()
	}
	session, err := s.CreateInTx(ctx, tx, userID)
	if err != nil {
		return Session{}, err
	}
	if err := s.RevokeInTx(ctx, tx, prelogin.ID); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, sessionFailure(err)
	}
	return session, nil
}
