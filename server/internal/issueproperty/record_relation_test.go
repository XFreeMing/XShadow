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

func TestValidateRecordRelationValue(t *testing.T) {
	tableID := uuid.NewString()
	config, _ := json.Marshal(RecordRelationConfig{TargetTableID: tableID})
	def := db.IssueProperty{Type: "record_relation", Config: config}
	recordID := uuid.NewString()
	stored, err := ValidateValue(def, json.RawMessage(`["`+recordID+`"]`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), recordID) {
		t.Fatalf("stored = %s", stored)
	}
	if _, err := ValidateValue(def, json.RawMessage(`[]`)); err == nil {
		t.Fatal("empty array accepted")
	}
}
