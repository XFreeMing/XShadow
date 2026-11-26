package issueproperty

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// LookupConfig is the stored definition config for lookup properties.
type LookupConfig struct {
	RelationPropertyID string `json:"relation_property_id"`
	TargetFieldID      string `json:"target_field_id"`
}

// IsLookup reports whether values are projected from a record_relation chain.
func IsLookup(propertyType string) bool {
	return propertyType == "lookup"
}

// ParseLookupConfig validates input or stored lookup configuration.
func ParseLookupConfig(raw []byte) (LookupConfig, error) {
	var cfg LookupConfig
	if len(raw) == 0 {
		return LookupConfig{}, errors.New("lookup properties require relation_property_id and target_field_id in config")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return LookupConfig{}, errors.New("config must be a JSON object")
	}
	for key := range object {
		if key != "relation_property_id" && key != "target_field_id" {
			return LookupConfig{}, fmt.Errorf("lookup config does not accept %q", key)
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return LookupConfig{}, errors.New("config must be a JSON object")
	}
	rel := strings.TrimSpace(cfg.RelationPropertyID)
	if rel == "" {
		return LookupConfig{}, errors.New("lookup properties require relation_property_id in config")
	}
	if _, err := uuid.Parse(rel); err != nil {
		return LookupConfig{}, errors.New("relation_property_id must be a UUID")
	}
	target := strings.TrimSpace(cfg.TargetFieldID)
	if target == "" {
		return LookupConfig{}, errors.New("lookup properties require target_field_id in config")
	}
	if _, err := uuid.Parse(target); err != nil {
		return LookupConfig{}, errors.New("target_field_id must be a UUID")
	}
	cfg.RelationPropertyID = uuid.MustParse(rel).String()
	cfg.TargetFieldID = uuid.MustParse(target).String()
	return cfg, nil
}