package tables

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	testWSID    = "11111111-1111-4111-8111-111111111111"
	testTblSrc  = "11111111-1111-4111-8111-111111111111"
	testTblNew  = "22222222-2222-4222-8222-222222222222"
	testTblExt  = "33333333-3333-4333-8333-333333333333"
	testFldOld1 = "aaaa1111-1111-4111-8111-111111111111"
	testFldOld2 = "aaaa2222-2222-4222-8222-222222222222"
	testFldOld3 = "aaaa3333-3333-4333-8333-333333333333"
	testFldOld4 = "aaaa4444-4444-4444-8444-444444444444"
	testFldNew1 = "bbbb1111-1111-4111-8111-111111111111"
	testFldNew2 = "bbbb2222-2222-4222-8222-222222222222"
	testFldNew3 = "bbbb3333-3333-4333-8333-333333333333"
	testFldNew4 = "bbbb4444-4444-4444-8444-444444444444"
	testRec1    = "33333333-3333-4333-8333-333333333333"
	testRec2    = "44444444-4444-4444-8444-444444444444"
)

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }
func i32Ptr(v int32) *int32   { return &v }

func TestValidateCleanNameAndConfigs(t *testing.T) {
	if _, err := CleanName("   "); err == nil {
		t.Fatal("expected empty name to fail")
	}
	got, err := CleanName("  需求台账  ")
	if err != nil || got != "需求台账" {
		t.Fatalf("CleanName = %q, %v", got, err)
	}
	if _, err := NormalizeConfig("unknown", map[string]any{}); err == nil {
		t.Fatal("expected unknown kind to fail")
	}
	if _, err := NormalizeConfig("single_select", map[string]any{"options": []any{}}); err == nil {
		t.Fatal("expected empty single_select options to fail")
	}
	cfg, err := NormalizeConfig("single_select", map[string]any{
		"options": []any{
			map[string]any{"id": "todo", "label": "待办"},
			map[string]any{"id": "done", "label": "完成"},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig single_select: %v", err)
	}
	def := FieldDef{ID: testFldOld1, Kind: "single_select", Config: cfg}
	val, valid, err := NormalizeValue(def, "todo")
	if err != nil || !valid || val != "todo" {
		t.Fatalf("NormalizeValue single_select = %v, %v, %v", val, valid, err)
	}
	if _, _, err := NormalizeValue(def, "invalid"); err == nil {
		t.Fatal("expected invalid single_select option to fail")
	}

	dateDef := FieldDef{ID: testFldOld2, Kind: "date", Config: map[string]any{}}
	if _, _, err := NormalizeValue(dateDef, "2026-02-29"); err == nil {
		t.Fatal("expected 2026-02-29 non-leap date to fail")
	}
	if val, valid, err := NormalizeValue(dateDef, "2024-02-29"); err != nil || !valid || val != "2024-02-29" {
		t.Fatalf("leap date failed: %v %v %v", val, valid, err)
	}
}

func TestCollabDecideDigestAndBatchPageTrim(t *testing.T) {
	fieldSet := func(names ...string) map[string]struct{} {
		m := make(map[string]struct{}, len(names))
		for _, n := range names {
			m[n] = struct{}{}
		}
		return m
	}
	if d := Decide(4, 4, nil, fieldSet("a")); !d.Apply || d.Base != 4 {
		t.Fatalf("same revision decision = %+v", d)
	}
	steps := []revisionStep{{base: 1, result: 2, fields: fieldSet("a")}}
	if d := Decide(2, 1, steps, fieldSet("b")); !d.Apply || d.Base != 2 {
		t.Fatalf("disjoint replay decision = %+v", d)
	}
	if d := Decide(2, 1, steps, fieldSet("a")); d.Apply {
		t.Fatalf("overlap should conflict: %+v", d)
	}
	if d := Decide(3, 1, steps, fieldSet("b")); d.Apply {
		t.Fatalf("gap should conflict: %+v", d)
	}

	digest := DigestStr("record.deleted:11111111-1111-4111-8111-111111111111:2")
	if len(digest) != 32 {
		t.Fatalf("digest len = %d", len(digest))
	}

	makeChange := func(seq int64, batchID *string) ChangeDto {
		return ChangeDto{Seq: seq, BatchID: batchID}
	}
	rows := []ChangeDto{
		makeChange(1, nil),
		makeChange(2, strPtr("batch-a")),
		makeChange(3, strPtr("batch-a")),
		makeChange(4, strPtr("batch-b")),
		makeChange(5, strPtr("batch-b")),
		makeChange(6, strPtr("batch-b")),
	}
	page := trimSplitBatchPage(rows, 5)
	if len(page) != 3 || page[2].Seq != 3 {
		t.Fatalf("trimSplitBatchPage kept %d items, last=%v", len(page), page)
	}
}

func TestBatchRequestParseAndCanonicalBytes(t *testing.T) {
	if _, err := ParseBatchRequest(map[string]any{
		"idempotency_key": testWSID,
		"operation":       "create",
		"records":         []any{},
	}); err == nil {
		t.Fatal("expected empty batch to fail")
	}

	dupUpdate := map[string]any{
		"idempotency_key": testWSID,
		"operation":       "update",
		"records": []any{
			map[string]any{"id": testRec1, "expected_revision": float64(1), "values": map[string]any{testFldOld1: "a"}},
			map[string]any{"id": strings.ToUpper(testRec1), "expected_revision": float64(1), "values": map[string]any{testFldOld1: "b"}},
		},
	}
	_, berr := ParseBatchRequest(dupUpdate)
	if berr == nil || berr.Index == nil || *berr.Index != 1 || berr.RecordID == nil || *berr.RecordID != testRec1 {
		t.Fatalf("unexpected dup update error: %+v", berr)
	}

	a, berr := ParseBatchRequest(map[string]any{
		"idempotency_key": testWSID,
		"operation":       "update",
		"records": []any{
			map[string]any{"id": testRec1, "expected_revision": float64(2), "values": map[string]any{testFldOld1: "ok"}},
		},
	})
	if berr != nil {
		t.Fatalf("parse a: %v", berr)
	}
	b, berr := ParseBatchRequest(map[string]any{
		"idempotency_key": strings.ToUpper(testWSID),
		"operation":       "update",
		"records": []any{
			map[string]any{"values": map[string]any{strings.ToUpper(testFldOld1): "ok"}, "expected_revision": float64(2), "id": strings.ToUpper(testRec1)},
		},
	})
	if berr != nil {
		t.Fatalf("parse b: %v", berr)
	}
	ba, err := a.CanonicalRequestBytes()
	if err != nil {
		t.Fatal(err)
	}
	bb, err := b.CanonicalRequestBytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(ba) != string(bb) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", ba, bb)
	}
}

func TestCommentsValidation(t *testing.T) {
	if _, err := CleanCommentBody("   \n\t "); err == nil {
		t.Fatal("expected blank comment body to fail")
	}
	body, err := CleanCommentBody("  你好\n世界  ")
	if err != nil || body != "你好\n世界" {
		t.Fatalf("CleanCommentBody = %q, %v", body, err)
	}
	if _, err := CleanCommentBody(strings.Repeat("a", maxCommentBodyChars+1)); err == nil {
		t.Fatal("expected overlong comment body to fail")
	}
	after, limit, err := NormalizeCommentListParams(nil, nil)
	if err != nil || after != 0 || limit != defaultCommentLimit {
		t.Fatalf("default params = %d, %d, %v", after, limit, err)
	}
	if _, _, err := NormalizeCommentListParams(i64Ptr(-1), nil); err == nil {
		t.Fatal("expected negative cursor to fail")
	}
	if _, _, err := NormalizeCommentListParams(nil, i64Ptr(101)); err == nil {
		t.Fatal("expected limit > 100 to fail")
	}
}

func TestHistoryParsingAndSnapshotAssembly(t *testing.T) {
	if n, err := ParseHistoryLimit(nil); err != nil || n != defaultHistoryLimit {
		t.Fatalf("ParseHistoryLimit(nil) = %d, %v", n, err)
	}
	if _, err := ParseHistoryLimit(i64Ptr(0)); err == nil {
		t.Fatal("expected limit=0 to fail")
	}
	if cur, err := parseDeletedCursor(strPtr("42:" + testWSID)); err != nil || cur.revision != 42 || cur.recordID == nil || *cur.recordID != testWSID {
		t.Fatalf("composite cursor = %+v, %v", cur, err)
	}
	if _, err := parseDeletedCursor(strPtr("42:not-a-uuid")); err == nil {
		t.Fatal("expected bad composite cursor to fail")
	}
	tgt, err := ParseSnapshotTarget(nil, strPtr("2026-11-26T09:00:00.654321+08:00"))
	if err != nil || tgt.Timestamp == nil || *tgt.Timestamp != "2026-11-26T01:00:00.654321Z" {
		t.Fatalf("timestamp target = %+v, %v", tgt, err)
	}
	if _, err := ParseSnapshotTarget(i64Ptr(1), strPtr("2026-11-26T09:00:00Z")); err == nil {
		t.Fatal("expected both revision and timestamp to fail")
	}

	zero := "00000000-0000-0000-0000-000000000000"
	snap, herr := assembleHistorySnapshot(3, "2026-11-26T01:05:00.000000Z", false, []versionEntry{
		{
			kind: "table", entityID: testTblSrc, secondaryID: zero, hasPayload: true,
			payload: map[string]any{"id": testTblSrc, "name": "Orders", "created_at": "2026-11-26T01:00:00.123456+00:00", "updated_at": "2026-11-26T01:05:00+00:00"},
		},
		{
			kind: "field", entityID: testFldOld1, secondaryID: zero, hasPayload: true,
			payload: map[string]any{"id": testFldOld1, "name": "Title", "kind": "text", "config": map[string]any{}, "position": float64(1), "created_at": "2026-11-26T01:00:00Z", "updated_at": "2026-11-26T01:00:00Z"},
		},
		{
			kind: "record", entityID: testRec1, secondaryID: zero, hasPayload: true,
			payload: map[string]any{"id": testRec1, "revision": float64(2), "created_at": "2026-11-26T01:00:00Z", "updated_at": "2026-11-26T01:02:00Z"},
		},
		{
			kind: "record", entityID: testRec2, secondaryID: zero, hasPayload: false,
		},
		{
			kind: "cell", entityID: testRec1, secondaryID: testFldOld1, hasPayload: true,
			payload: map[string]any{"value": "Live Row"},
		},
	})
	if herr != nil {
		t.Fatalf("assembleHistorySnapshot: %v", herr)
	}
	if snap.ComputedValuesMode != "stored_raw_only" || len(snap.Records) != 1 || snap.Records[0].Values[testFldOld1] != "Live Row" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

func TestTemplateRebindAndCycleDetection(t *testing.T) {
	fields := []TemplateFieldInput{
		{ID: strPtr(testFldOld1), Name: "Price", Kind: "number", Position: i32Ptr(1), Config: map[string]any{}},
		{ID: strPtr(testFldOld2), Name: "Parent", Kind: "relation", Position: i32Ptr(2), Config: map[string]any{"target_table_id": testTblSrc, "bidirectional": false}},
		{ID: strPtr(testFldOld3), Name: "ParentPrice", Kind: "lookup", Position: i32Ptr(3), Config: map[string]any{"relation_field_id": testFldOld2, "target_field_id": testFldOld1}},
		{ID: strPtr(testFldOld4), Name: "Total", Kind: "formula", Position: i32Ptr(4), Config: map[string]any{"expression": "{Price} + SUM({ParentPrice})"}},
	}
	prepared, terr := prepareTemplateFields(fields)
	if terr != nil {
		t.Fatalf("prepareTemplateFields: %v", terr)
	}
	newIDs := []string{testFldNew1, testFldNew2, testFldNew3, testFldNew4}
	idMap := map[string]string{
		testFldOld1: testFldNew1,
		testFldOld2: testFldNew2,
		testFldOld3: testFldNew3,
		testFldOld4: testFldNew4,
	}
	resolved, terr := resolveAndValidateTemplateFields(testTblNew, strPtr(testTblSrc), prepared, newIDs, idMap)
	if terr != nil {
		t.Fatalf("resolveAndValidateTemplateFields: %v", terr)
	}
	relMap := resolved[1].(map[string]any)
	if relMap["target_table_id"] != testTblNew {
		t.Fatalf("relation not rebound to new table: %v", relMap)
	}
	lkpMap := resolved[2].(map[string]any)
	if lkpMap["relation_field_id"] != testFldNew2 || lkpMap["target_field_id"] != testFldNew1 {
		t.Fatalf("lookup not rebound: %v", lkpMap)
	}
	fMap := resolved[3].(map[string]any)
	refsJSON, _ := json.Marshal(fMap["refs"])
	if !strings.Contains(string(refsJSON), testFldNew1) || !strings.Contains(string(refsJSON), testFldNew3) {
		t.Fatalf("formula refs not rebound: %s", refsJSON)
	}

	// Cycle detection
	cyclic := []TemplateFieldInput{
		{ID: strPtr(testFldOld1), Name: "A", Kind: "formula", Config: map[string]any{"expression": "{B} + 1"}},
		{ID: strPtr(testFldOld2), Name: "B", Kind: "formula", Config: map[string]any{"expression": "{A} + 1"}},
	}
	prepCyclic, terr := prepareTemplateFields(cyclic)
	if terr != nil {
		t.Fatal(terr)
	}
	if _, terr := resolveAndValidateTemplateFields(testTblNew, nil, prepCyclic, []string{testFldNew1, testFldNew2}, idMap); terr == nil {
		t.Fatal("expected cycle error")
	}

	// Sample record IDs parsing
	ids, terr := ParseSampleRecordIDsQuery(strPtr(strings.ToUpper(testFldOld1) + ", " + testFldOld1))
	if terr != nil || len(ids) != 1 || ids[0] != testFldOld1 {
		t.Fatalf("ParseSampleRecordIDsQuery = %v, %v", ids, terr)
	}
}

func TestIssueDerivedTopoOrderAndCycleDetection(t *testing.T) {
	defs := []issuePropertyDef{
		{id: "b", name: "B", kind: "formula", config: map[string]any{"ast": map[string]any{"Field": "a"}}},
		{id: "a", name: "A", kind: "lookup", config: map[string]any{"relation_property_id": "rel", "target_field_id": "fld"}},
	}
	order := topoDerivedProperties(defs)
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("topo order = %v, want [a b]", order)
	}
	graph := map[string][]string{"a": {"b"}, "b": {"a"}}
	if !hasCycleFrom(graph, "a") {
		t.Fatal("expected cycle")
	}
}

func TestHTTPHandlerAuthAndViewBodyParsing(t *testing.T) {
	// Missing pool -> 503
	hNil := NewHandler(nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tables", nil)
	hNil.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil pool status = %d", rec.Code)
	}

	// View body omits kind and config -> keeps existing
	var legacy viewBody
	if err := json.Unmarshal([]byte(`{"name":"旧视图"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	w := legacy.toViewWrite()
	if w.Kind != nil || w.Config.Set || w.Group.Set {
		t.Fatalf("legacy view write = %+v", w)
	}

	var explicit viewBody
	if err := json.Unmarshal([]byte(`{"name":"看板","kind":"kanban","config":{},"group":null}`), &explicit); err != nil {
		t.Fatal(err)
	}
	w2 := explicit.toViewWrite()
	if w2.Kind == nil || *w2.Kind != "kanban" || !w2.Config.Set || !w2.Group.Set || w2.Group.Value != nil {
		t.Fatalf("explicit view write = %+v", w2)
	}

	// Trusted actor resolution from task_token vs member context
	h := NewHandler(nil, func(_ context.Context, _, _ string) string { return "核对智能体" })
	agentReq := httptest.NewRequest(http.MethodPost, "/api/tables", nil)
	agentReq.Header.Set("X-Actor-Source", "task_token")
	agentReq.Header.Set("X-Agent-ID", testFldOld1)
	agentReq.Header.Set("X-Task-ID", testFldOld2)
	actor, ok := h.resolveTrustedActor(agentReq, testWSID)
	if !ok || actor.Source != "agent" || actor.Label != "核对智能体" || actor.TaskID == nil || *actor.TaskID != testFldOld2 {
		t.Fatalf("agent actor = %+v, ok=%v", actor, ok)
	}

	userUUID, _ := util.ParseUUID(testFldNew1)
	memberReq := httptest.NewRequest(http.MethodPost, "/api/tables", nil)
	memberReq = memberReq.WithContext(middleware.SetMemberContext(memberReq.Context(), testWSID, db.Member{UserID: userUUID}))
	mActor, ok := h.resolveTrustedActor(memberReq, testWSID)
	if !ok || mActor.Source != "member" || mActor.ID != testFldNew1 || !strings.HasPrefix(mActor.Label, "成员") {
		t.Fatalf("member actor = %+v, ok=%v", mActor, ok)
	}
}
