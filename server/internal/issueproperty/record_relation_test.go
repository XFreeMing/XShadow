package issueproperty

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParseRecordIDListPreservesOrder(t *testing.T) {
	first := uuid.NewString()
	second := uuid.NewString()
	got, err := ParseRecordIDList([]any{first, second, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("got %v", got)
	}
}

func TestParseRecordRelationConfig(t *testing.T) {
	tableID := uuid.NewString()
	cfg, err := ParseRecordRelationConfig([]byte(`{"target_table_id":"` + strings.ToUpper(tableID) + `"}`))
	if err != nil {
		t.Fatalf("table target: %v", err)
	}
	if cfg.TargetTableID != tableID || cfg.TargetKind != "" || cfg.IsIssuesTarget() {
		t.Fatalf("unexpected table config: %+v", cfg)
	}

	issuesCfg, err := ParseRecordRelationConfig([]byte(`{"target_kind":"issues"}`))
	if err != nil {
		t.Fatalf("issues target: %v", err)
	}
	if !issuesCfg.IsIssuesTarget() || issuesCfg.TargetTableID != "" || issuesCfg.TargetKind != RecordRelationTargetKindIssues {
		t.Fatalf("unexpected issues config: %+v", issuesCfg)
	}
	if cfg.SameTarget(issuesCfg) || !issuesCfg.SameTarget(RecordRelationConfig{TargetKind: "issues"}) {
		t.Fatalf("SameTarget mismatch")
	}

	for _, raw := range []string{
		`{}`,
		`{"target_table_id":"` + tableID + `","target_kind":"issues"}`,
		`{"target_kind":"projects"}`,
		`{"target_table_id":"not-a-uuid"}`,
		`{"target_kind":"issues","extra":true}`,
	} {
		if _, err := ParseRecordRelationConfig([]byte(raw)); err == nil {
			t.Fatalf("expected error for %s", raw)
		}
	}
}

func TestValidateRecordRelationValue(t *testing.T) {
	tableID := uuid.NewString()
	for _, cfg := range []RecordRelationConfig{
		{TargetTableID: tableID},
		{TargetKind: RecordRelationTargetKindIssues},
	} {
		config, _ := json.Marshal(cfg)
		def := db.IssueProperty{Type: "record_relation", Config: config}
		first := uuid.NewString()
		second := uuid.NewString()
		stored, err := ValidateValue(def, json.RawMessage(`["`+first+`","`+second+`","`+first+`"]`))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(stored, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0] != first || got[1] != second {
			t.Fatalf("stored = %s", stored)
		}
		if _, err := ValidateValue(def, json.RawMessage(`[]`)); err == nil {
			t.Fatal("empty array accepted")
		}
	}
}
