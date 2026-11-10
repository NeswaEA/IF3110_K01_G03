package platform

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestOpenAPISpecIsValidAndListsEveryErrorCode(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, code := range doc.Components.Schemas["ErrorCode"].Value.Enum {
		documented = append(documented, code.(string))
	}
	for code := range errorStatuses {
		if !slices.Contains(documented, code) {
			t.Errorf("error code %s is missing from the spec", code)
		}
	}
	for _, code := range documented {
		if _, ok := errorStatuses[code]; !ok {
			t.Errorf("spec lists %s, which the server never maps", code)
		}
	}
}
