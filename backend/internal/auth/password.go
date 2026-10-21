package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"golang.org/x/crypto/argon2"
)

const passwordHashPrefix = "$argon2id$v=19$m=19456,t=2,p=1$"

var ErrInvalidPasswordHash = errors.New("invalid password hash")
var passwordHashSlots = make(chan struct{}, 2)

type PasswordHasher struct{}

func (PasswordHasher) Hash(ctx context.Context, password string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if err := acquirePasswordHashSlot(ctx); err != nil {
		return "", err
	}
	defer func() { <-passwordHashSlots }()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("password hashing failed")
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return passwordHashPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}

func (PasswordHasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(password) == 0 || len(password) > 1024 || !utf8.ValidString(password) {
		return false, nil
	}
	if len(encoded) != len(passwordHashPrefix)+22+1+43 || !strings.HasPrefix(encoded, passwordHashPrefix) {
		return false, ErrInvalidPasswordHash
	}
	saltText, hashText, ok := strings.Cut(encoded[len(passwordHashPrefix):], "$")
	if !ok || len(saltText) != 22 || len(hashText) != 43 {
		return false, ErrInvalidPasswordHash
	}
	encoding := base64.RawStdEncoding.Strict()
	salt, err := encoding.DecodeString(saltText)
	if err != nil || len(salt) != 16 {
		return false, ErrInvalidPasswordHash
	}
	want, err := encoding.DecodeString(hashText)
	if err != nil || len(want) != 32 {
		return false, ErrInvalidPasswordHash
	}
	if err := acquirePasswordHashSlot(ctx); err != nil {
		return false, err
	}
	defer func() { <-passwordHashSlots }()
	got := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func acquirePasswordHashSlot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case passwordHashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return &contracts.DomainError{Code: "RATE_LIMITED", Message: "Pemrosesan password sedang penuh. Coba lagi."}
	}
}
