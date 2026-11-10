package migrations

import "github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"

func problem(code, message string) *contracts.DomainError {
	return &contracts.DomainError{Code: code, Message: message}
}

func internal(err error) error {
	return &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Operasi migrasi tidak dapat diselesaikan.", Cause: err}
}
