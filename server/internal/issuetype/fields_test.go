package issuetype

import (
	"encoding/json"
	"testing"
)

func TestFieldHasDefaultDistinguishesFalsyFromAbsent(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "zero number", raw: `0`, want: true},
		{name: "false boolean", raw: `false`, want: true},
		{name: "string", raw: `"hello"`, want: true},
		{name: "null", raw: `null`, want: false},
		{name: "empty", raw: ``, want: false},
		{name: "whitespace", raw: `   `, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := Field{PropertyID: "prop-1", DefaultValue: json.RawMessage(tc.raw)}
			if got := f.HasDefault(); got != tc.want {
				t.Fatalf("HasDefault(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseFieldsRoundTripsOrderedDefaults(t *testing.T) {
	raw := []byte(`[
		{"property_id":"p1","required":true},
		{"property_id":"p2","required":false,"default_value":0},
		{"property_id":"p3","required":false,"default_value":false}
	]`)
	fields, err := ParseFields(raw)
	if err != nil {
		t.Fatalf("ParseFields: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("len(fields) = %d, want 3", len(fields))
	}
	if fields[0].HasDefault() {
		t.Fatalf("fields[0] unexpectedly has default: %s", string(fields[0].DefaultValue))
	}
	if !fields[1].HasDefault() || string(fields[1].DefaultValue) != "0" {
		t.Fatalf("fields[1] default = %q, want 0", string(fields[1].DefaultValue))
	}
	if !fields[2].HasDefault() || string(fields[2].DefaultValue) != "false" {
		t.Fatalf("fields[2] default = %q, want false", string(fields[2].DefaultValue))
	}
}
