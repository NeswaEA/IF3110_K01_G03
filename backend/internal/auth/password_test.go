package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
	"golang.org/x/crypto/argon2"
)

func TestPasswordHashParametersAndVerification(t *testing.T) {
	ctx := context.Background()
	hasher := PasswordHasher{}
	password := "  Example-password-123  "
	encoded, err := hasher.Hash(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
		t.Fatal("unexpected PHC algorithm or parameters")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		t.Fatal("invalid salt encoding")
	}
	hash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(hash) != 32 {
		t.Fatal("invalid hash encoding")
	}
	want := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	if !bytes.Equal(hash, want) {
		t.Fatal("encoded hash does not match the declared parameters")
	}
	for _, tt := range []struct {
		password string
		want     bool
	}{
		{password, true}, {strings.TrimSpace(password), false}, {"Wrong-password-123", false}, {"short", false},
	} {
		ok, err := hasher.Verify(ctx, tt.password, encoded)
		if err != nil || ok != tt.want {
			t.Fatalf("verification mismatch: %v, %v", ok, err)
		}
	}
	second, err := hasher.Hash(ctx, password)
	if err != nil || second == encoded {
		t.Fatal("salt was reused")
	}
	if len(passwordHashSlots) != 0 {
		t.Fatal("hashing slot was not released")
	}
}

func TestMalformedPasswordHashes(t *testing.T) {
	valid := passwordHashPrefix + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)
	for _, encoded := range []string{
		"", strings.Repeat("x", 1<<20),
		strings.Replace(valid, "argon2id", "argon2i", 1),
		strings.Replace(valid, "v=19", "v=16", 1),
		strings.Replace(valid, "m=19456", "m=999999999999999999", 1),
		strings.Replace(valid, "t=2", "t=9", 1),
		strings.Replace(valid, "p=1", "p=9", 1),
		valid + "=", valid + "$extra",
		passwordHashPrefix + strings.Repeat("!", 22) + "$" + strings.Repeat("A", 43),
		passwordHashPrefix + strings.Repeat("A", 22) + "$" + strings.Repeat("!", 43),
		passwordHashPrefix + strings.Repeat("A", 21) + "B$" + strings.Repeat("A", 43),
		passwordHashPrefix + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 42) + "B",
	} {
		ok, err := (PasswordHasher{}).Verify(context.Background(), "Example-password-123", encoded)
		if ok || !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatal("malformed or excessive-cost hash accepted")
		}
	}
}

func TestPasswordInputBoundsAndCancellation(t *testing.T) {
	hasher := PasswordHasher{}
	if encoded, err := hasher.Hash(context.Background(), "short"); err == nil || encoded != "" {
		t.Fatal("short password hashed")
	}
	for _, password := range []string{"", strings.Repeat("a", 1025), "invalid\xff"} {
		if ok, err := hasher.Verify(context.Background(), password, "invalid"); ok || err != nil {
			t.Fatal("invalid login input did not return a mismatch")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if encoded, err := hasher.Hash(ctx, "Example-password-123"); encoded != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled hash request accepted")
	}
	if ok, err := hasher.Verify(ctx, "Example-password-123", "invalid"); ok || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled verification accepted")
	}
	if len(passwordHashSlots) != 0 {
		t.Fatal("canceled request leaked a hashing slot")
	}
}

func TestHashingLimitIsSharedAcrossInstances(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := acquirePasswordHashSlot(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { <-passwordHashSlots })
	}
	valid := passwordHashPrefix + strings.Repeat("A", 22) + "$" + strings.Repeat("A", 43)
	_, hashErr := (PasswordHasher{}).Hash(ctx, "Example-password-123")
	_, verifyErr := (PasswordHasher{}).Verify(ctx, "Example-password-123", valid)
	for _, err := range []error{hashErr, verifyErr} {
		var domain *contracts.DomainError
		if !errors.As(err, &domain) || domain.Code != "RATE_LIMITED" {
			t.Fatal("excess hashing work was not rejected")
		}
	}
	if _, err := (PasswordHasher{}).Verify(ctx, "Example-password-123", "invalid"); !errors.Is(err, ErrInvalidPasswordHash) {
		t.Fatal("malformed hash was not rejected before acquiring a slot")
	}
}

func BenchmarkPasswordHash(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := (PasswordHasher{}).Hash(context.Background(), "Example-password-123"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPasswordVerify(b *testing.B) {
	ctx := context.Background()
	hasher := PasswordHasher{}
	encoded, err := hasher.Hash(ctx, "Example-password-123")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ok, err := hasher.Verify(ctx, "Example-password-123", encoded); err != nil || !ok {
			b.Fatal("verification failed")
		}
	}
}
