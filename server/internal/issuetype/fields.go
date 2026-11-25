// Package issuetype owns the transport-independent field schema for
// workspace-configured issue types. Both the HTTP definition handlers and
// IssueService.Create read this representation so defaults, ordering, and
// requiredness stay consistent across write paths.
package issuetype

import (
	"bytes"
	"encoding/json"
)

// Field links one workspace issue_property definition into an issue type in
// display order, with optional requiredness and a typed optional default value.
type Field struct {
	PropertyID   string          `json:"property_id"`
	Required     bool            `json:"required"`
	DefaultValue json.RawMessage `json:"default_value,omitempty"`
}

// HasDefault reports whether this field carries an explicit non-null default.
// Legitimate falsy JSON values like 0 and false return true.
func (f Field) HasDefault() bool {
	trimmed := bytes.TrimSpace(f.DefaultValue)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// ParseFields decodes the JSONB array stored on issue_type.fields.
func ParseFields(raw []byte) ([]Field, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []Field{}, nil
	}
	var fields []Field
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return []Field{}, nil
	}
	return fields, nil
}
