package issueproperty

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// FormulaConfig is the stored definition config for formula properties.
// The Rust compiler persists expression, ast, and refs; API responses hydrate
// expression only for editing.
type FormulaConfig struct {
	Expression string          `json:"expression"`
	AST        json.RawMessage `json:"ast,omitempty"`
	Refs       []string        `json:"refs,omitempty"`
}

// IsFormula reports whether values are projected from a same-task expression.
func IsFormula(propertyType string) bool {
	return propertyType == "formula"
}

// ParseFormulaInputConfig validates create/update input (expression only).
func ParseFormulaInputConfig(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("formula properties require expression in config")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", errors.New("config must be a JSON object")
	}
	for key := range object {
		if key != "expression" {
			return "", fmt.Errorf("formula config does not accept %q", key)
		}
	}
	var expression string
	if err := json.Unmarshal(object["expression"], &expression); err != nil {
		return "", errors.New("formula properties require expression in config")
	}
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return "", errors.New("formula expression cannot be empty")
	}
	return expression, nil
}

// ParseFormulaStoredConfig reads a persisted formula definition.
func ParseFormulaStoredConfig(raw []byte) (FormulaConfig, error) {
	var cfg FormulaConfig
	if len(raw) == 0 {
		return FormulaConfig{}, errors.New("formula config is invalid")
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return FormulaConfig{}, errors.New("formula config is invalid")
	}
	if cfg.AST == nil {
		return FormulaConfig{}, errors.New("formula config is missing ast")
	}
	return cfg, nil
}