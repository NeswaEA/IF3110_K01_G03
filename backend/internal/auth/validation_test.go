package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

func TestNormalizeUsername(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{" Editor.Demo_1-2 ", "editor.demo_1-2"},
		{"\teditor\n", "editor"},
		{"abc", "abc"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
	} {
		got, err := NormalizeUsername(tt.input)
		if err != nil || got != tt.want {
			t.Fatalf("unexpected normalization: %q, %v", got, err)
		}
	}
	for _, input := range []string{"", "ab", strings.Repeat("a", 65), "with space", "user@example", "中文用户名", "invalid\xff"} {
		got, err := NormalizeUsername(input)
		var domain *contracts.DomainError
		if got != "" || !errors.As(err, &domain) || domain.Code != "INVALID_REQUEST" || domain.Fields["username"] == "" {
			t.Fatal("invalid username accepted or unsafe error returned")
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for _, password := range []string{strings.Repeat("a", 12), strings.Repeat("a", 128), strings.Repeat("界", 12), strings.Repeat("🙂", 128), "  keep spaces  "} {
		if err := ValidatePassword(password); err != nil {
			t.Fatal(err)
		}
	}
	for _, password := range []string{"", strings.Repeat("a", 11), strings.Repeat("a", 129), strings.Repeat("界", 11), strings.Repeat("a", 1025), "password-secret\xff"} {
		err := ValidatePassword(password)
		var domain *contracts.DomainError
		if !errors.As(err, &domain) || domain.Code != "INVALID_REQUEST" || domain.Fields["password"] == "" {
			t.Fatal("invalid password accepted")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(domain.Fields["password"], "secret") {
			t.Fatal("password leaked in validation error")
		}
	}
}
