package contracts_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

func TestDomainErrorPreservesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New("password=secret")
	err := &contracts.DomainError{Code: "INTERNAL_ERROR", Message: "Operation failed", Cause: cause}
	wrapped := fmt.Errorf("request: %w", err)
	if err.Error() != "Operation failed" || strings.Contains(wrapped.Error(), "secret") {
		t.Fatal("error text exposed the cause")
	}
	var domain *contracts.DomainError
	if !errors.Is(wrapped, cause) || !errors.As(wrapped, &domain) || domain != err {
		t.Fatal("error chain lost the domain error or cause")
	}
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(data), "Cause") || strings.Contains(string(data), "secret") {
		t.Fatal("JSON exposed the cause")
	}
	if (&contracts.DomainError{}).Unwrap() != nil {
		t.Fatal("absent cause must unwrap to nil")
	}
}
