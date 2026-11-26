package handler

import (
	"encoding/json"
	"testing"
)

func TestFilterProjectedComputedPropertiesDropsUnknownKeys(t *testing.T) {
	allowed := map[string]struct{}{"issue-a": {}}
	computed := map[string]struct{}{"field-computed": {}}
	in := map[string]map[string]json.RawMessage{
		"issue-a": {
			"field-computed": json.RawMessage(`[1,2]`),
			"field-editable": json.RawMessage(`"x"`),
		},
		"issue-b": {
			"field-computed": json.RawMessage(`true`),
		},
	}
	out := filterProjectedComputedProperties(in, allowed, computed)
	if len(out) != 1 {
		t.Fatalf("expected one issue, got %d", len(out))
	}
	bag := out["issue-a"]
	if len(bag) != 1 {
		t.Fatalf("expected one computed field, got %d", len(bag))
	}
	if _, ok := bag["field-computed"]; !ok {
		t.Fatal("expected computed field in output")
	}
}