package access

import (
	"net/http"
	"strconv"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

func parsePage(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	for name, value := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := r.URL.Query().Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return 0, 0, pageError(name, "Gunakan bilangan bulat.")
			}
			*value = n
		}
	}
	return limit, offset, validatePage(limit, offset)
}

func validatePage(limit, offset int) error {
	if limit < 1 || limit > 200 {
		return pageError("limit", "Gunakan bilangan bulat 1 sampai 200.")
	}
	if offset < 0 || offset > 100000 {
		return pageError("offset", "Gunakan bilangan bulat 0 sampai 100000.")
	}
	return nil
}

func pageError(field, message string) error {
	return &contracts.DomainError{Code: "INVALID_REQUEST", Message: "Parameter pagination tidak valid.", Fields: map[string]string{field: message}}
}
