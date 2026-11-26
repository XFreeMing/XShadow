package issueproperty

import (
	"encoding/json"
	"testing"
)

func TestParseLookupConfig(t *testing.T) {
	rel := "11111111-1111-4111-8111-111111111111"
	field := "22222222-2222-4222-8222-222222222222"
	raw, err := json.Marshal(map[string]string{
		"relation_property_id": rel,
		"target_field_id":      field,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseLookupConfig(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RelationPropertyID != rel || cfg.TargetFieldID != field {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseLookupConfigRejectsExtraKeys(t *testing.T) {
	raw := []byte(`{"relation_property_id":"11111111-1111-4111-8111-111111111111","target_field_id":"22222222-2222-4222-8222-222222222222","nope":1}`)
	if _, err := ParseLookupConfig(raw); err == nil {
		t.Fatal("expected error for extra key")
	}
}