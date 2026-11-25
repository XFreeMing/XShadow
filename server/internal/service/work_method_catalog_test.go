package service

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestWorkMethodCatalogFromOverridesMergesStoredTemplates(t *testing.T) {
	raw := []byte(`[{"id":"custom","name":"Custom","kind":"research","revision":2,"steps":[]}]`)
	methods, err := WorkMethodCatalogFromOverrides(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, method := range methods {
		if method.ID == "custom" && method.Revision == 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("custom template missing from catalog: %+v", methods)
	}
	if len(methods) < len(workmethod.Builtins()) {
		t.Fatalf("builtins should remain in catalog")
	}
}

func TestWorkMethodCatalogFromOverridesAcceptsNullOverrides(t *testing.T) {
	methods, err := WorkMethodCatalogFromOverrides([]byte("null"))
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != len(workmethod.Builtins()) {
		t.Fatalf("null overrides should return builtins only")
	}
}
