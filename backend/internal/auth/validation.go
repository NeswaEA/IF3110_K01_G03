package auth

import (
	"strings"
	"unicode/utf8"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

func NormalizeUsername(username string) (string, error) {
	if !utf8.ValidString(username) {
		return "", invalidUsername()
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 || len(username) > 64 {
		return "", invalidUsername()
	}
	for _, char := range username {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '.' && char != '-' {
			return "", invalidUsername()
		}
	}
	return username, nil
}

func invalidUsername() error {
	return &contracts.DomainError{
		Code:    "INVALID_REQUEST",
		Message: "Username tidak valid.",
		Fields:  map[string]string{"username": "Gunakan 3-64 karakter a-z, 0-9, titik, garis bawah, atau tanda minus."},
	}
}

func ValidatePassword(password string) error {
	if len(password) <= 1024 && utf8.ValidString(password) {
		length := utf8.RuneCountInString(password)
		if length >= 12 && length <= 128 {
			return nil
		}
	}
	return &contracts.DomainError{
		Code:    "INVALID_REQUEST",
		Message: "Password tidak valid.",
		Fields:  map[string]string{"password": "Gunakan 12-128 karakter UTF-8, maksimal 1024 byte."},
	}
}
