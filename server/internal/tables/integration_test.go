package tables

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func openIntegrationPool(t *testing.T) (*pgxpool.Pool, string, string, Actor) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(pool.Close)

	wsID := uuid.NewString()
	userID := uuid.NewString()
	slug := "tbl-test-" + wsID[:8]
	email := "tbl-" + userID[:8] + "@example.com"

	if _, err := pool.Exec(ctx,
		`INSERT INTO "user" (id, email, name) VALUES ($1::uuid, $2, 'Table Tester')`,
		userID, email,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, issue_prefix) VALUES ($1::uuid, 'Tables Test WS', $2, 'TBL')`,
		wsID, slug,
	); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'owner')`,
		wsID, userID,
	); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1::uuid`, wsID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1::uuid`, userID)
	})

	actor := Actor{
		Source: "member",
		ID:     userID,
		Label:  "测试成员",
	}
	return pool, wsID, userID, actor
}

func TestIntegrationTablesEndToEnd(t *testing.T) {
	pool, wsID, userID, actor := openIntegrationPool(t)
	ctx := context.Background()

	// 1. Create two tables (Projects & Orders) via store API.
	projCreated, err := CreateTable(ctx, pool, wsID, "项目台账")
	if err != nil {
		t.Fatalf("CreateTable projects: %v", err)
	}
	ordCreated, err := CreateTable(ctx, pool, wsID, "订单台账")
	if err != nil {
		t.Fatalf("CreateTable orders: %v", err)
	}

	tablesList, err := ListTables(ctx, pool, wsID)
	if err != nil || len(tablesList) != 2 {
		t.Fatalf("ListTables = %d, err=%v", len(tablesList), err)
	}
	if _, err := RenameTable(ctx, pool, wsID, ordCreated.Table.ID, "客户订单", &actor); err != nil {
		t.Fatalf("RenameTable: %v", err)
	}

	// 2. Add fields: text, number, single_select, bidirectional relation, lookup, formula.
	projName, err := CreateField(ctx, pool, wsID, projCreated.Table.ID, "项目名", "text", map[string]any{}, &actor)
	if err != nil {
		t.Fatalf("CreateField projName: %v", err)
	}
	projRate, err := CreateField(ctx, pool, wsID, projCreated.Table.ID, "单价", "number", map[string]any{}, &actor)
	if err != nil {
		t.Fatalf("CreateField projRate: %v", err)
	}

	ordTitle, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "标题", "text", map[string]any{}, &actor)
	if err != nil {
		t.Fatalf("CreateField ordTitle: %v", err)
	}
	ordQty, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "数量", "number", map[string]any{}, &actor)
	if err != nil {
		t.Fatalf("CreateField ordQty: %v", err)
	}
	ordStatus, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "状态", "single_select", map[string]any{
		"options": []any{
			map[string]any{"id": "open", "label": "进行中"},
			map[string]any{"id": "done", "label": "已完成"},
		},
	}, &actor)
	if err != nil {
		t.Fatalf("CreateField ordStatus: %v", err)
	}
	ordProjRel, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "所属项目", "relation", map[string]any{
		"target_table_id": projCreated.Table.ID,
		"bidirectional":   true,
	}, &actor)
	if err != nil {
		t.Fatalf("CreateField bidirectional relation: %v", err)
	}
	relCfg, err := relationConfigFromStored(ordProjRel.Config)
	if err != nil || !relCfg.Bidirectional || relCfg.ReverseFieldID == nil {
		t.Fatalf("unexpected bidirectional relation config: %+v, err=%v", relCfg, err)
	}

	ordRateLookup, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "项目单价", "lookup", map[string]any{
		"relation_field_id": ordProjRel.ID,
		"target_field_id":   projRate.ID,
	}, &actor)
	if err != nil {
		t.Fatalf("CreateField lookup: %v", err)
	}
	ordTotalFormula, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "总金额", "formula", map[string]any{
		"expression": "{数量} * SUM({项目单价})",
	}, &actor)
	if err != nil {
		t.Fatalf("CreateField formula: %v", err)
	}

	// Duplicate field name -> ErrKindNameTaken
	if _, err := CreateField(ctx, pool, wsID, ordCreated.Table.ID, "数量", "number", map[string]any{}, &actor); err == nil {
		t.Fatal("expected duplicate field name conflict")
	}

	// 3. Create records & verify bidirectional relation sync + lookup/formula projection.
	projRec, err := CreateRecord(ctx, pool, wsID, projCreated.Table.ID, map[string]any{
		projName.ID: "Alpha",
		projRate.ID: 25,
	}, &actor)
	if err != nil {
		t.Fatalf("CreateRecord proj: %v", err)
	}
	ordRec, err := CreateRecord(ctx, pool, wsID, ordCreated.Table.ID, map[string]any{
		ordTitle.ID:   "首单",
		ordQty.ID:     4,
		ordStatus.ID:  "open",
		ordProjRel.ID: []any{projRec.ID},
	}, &actor)
	if err != nil {
		t.Fatalf("CreateRecord ord: %v", err)
	}

	// Verify reverse field was updated on projRec.
	projFetched, err := GetRecord(ctx, pool, wsID, projCreated.Table.ID, projRec.ID)
	if err != nil {
		t.Fatalf("GetRecord proj: %v", err)
	}
	revIDs := extractRelationIDs(projFetched.Values[*relCfg.ReverseFieldID])
	if len(revIDs) != 1 || revIDs[0] != ordRec.ID {
		t.Fatalf("bidirectional reverse sync failed: got %v, want [%s]", revIDs, ordRec.ID)
	}

	// Verify lookup & formula projection on GetSheet and GetRecord.
	sheet, err := GetSheet(ctx, pool, wsID, ordCreated.Table.ID, nil)
	if err != nil {
		t.Fatalf("GetSheet: %v", err)
	}
	if len(sheet.Records) != 1 {
		t.Fatalf("expected 1 order record, got %d", len(sheet.Records))
	}
	gotTotal, ok := asFloat64(sheet.Records[0].Values[ordTotalFormula.ID])
	if !ok || gotTotal != 100 {
		t.Fatalf("projected formula total = %v, want 100 (lookup=%v)", sheet.Records[0].Values[ordTotalFormula.ID], sheet.Records[0].Values[ordRateLookup.ID])
	}

	// 4. Collab: disjoint merge succeeds, same-field edit conflicts.
	updated1, err := UpdateRecord(ctx, pool, wsID, ordCreated.Table.ID, ordRec.ID, 1, map[string]any{
		ordTitle.ID: "首单-已改名",
	}, actor)
	if err != nil || updated1.Revision != 2 {
		t.Fatalf("UpdateRecord rev1->2: rev=%d, err=%v", updated1.Revision, err)
	}
	// Stale client at revision 1 edits disjoint field ordQty -> merges cleanly to rev 3!
	updated2, err := UpdateRecord(ctx, pool, wsID, ordCreated.Table.ID, ordRec.ID, 1, map[string]any{
		ordQty.ID: 6,
	}, actor)
	if err != nil || updated2.Revision != 3 {
		t.Fatalf("disjoint replay rev1->3: rev=%d, err=%v", updated2.Revision, err)
	}
	// Stale client at revision 1 edits overlapping field ordTitle -> conflict!
	if _, err := UpdateRecord(ctx, pool, wsID, ordCreated.Table.ID, ordRec.ID, 1, map[string]any{
		ordTitle.ID: "冲突写入",
	}, actor); err == nil {
		t.Fatal("expected revision conflict on overlapping field")
	}

	changes, err := ListChangesSince(ctx, pool, wsID, ordCreated.Table.ID, 0)
	if err != nil || len(changes.Changes) == 0 {
		t.Fatalf("ListChangesSince: len=%d, err=%v", len(changes.Changes), err)
	}
	if _, err := TouchPresence(ctx, pool, wsID, ordCreated.Table.ID, actor, &ordRec.ID, &ordTitle.ID); err != nil {
		t.Fatalf("TouchPresence: %v", err)
	}
	pres, err := GetPresence(ctx, pool, wsID, ordCreated.Table.ID)
	if err != nil || len(pres.Presence) != 1 {
		t.Fatalf("GetPresence: %+v, err=%v", pres, err)
	}

	// 5. Comments & replies.
	c1, err := CreateComment(ctx, pool, wsID, ordCreated.Table.ID, ordRec.ID, actor, "第一条评论", nil)
	if err != nil || c1.Seq != 1 {
		t.Fatalf("CreateComment c1: %+v, err=%v", c1, err)
	}
	c2, err := CreateComment(ctx, pool, wsID, ordCreated.Table.ID, ordRec.ID, actor, "回复第一条", &c1.ID)
	if err != nil || c2.Seq != 2 || c2.ParentID == nil || *c2.ParentID != c1.ID {
		t.Fatalf("CreateComment c2: %+v, err=%v", c2, err)
	}
	cList, err := ListComments(ctx, pool, wsID, ordCreated.Table.ID, ordRec.ID, nil, nil)
	if err != nil || len(cList.Comments) != 2 {
		t.Fatalf("ListComments: %+v, err=%v", cList, err)
	}

	// 6. Batch create with idempotency replay & conflict.
	idemKey := uuid.NewString()
	batchReq, berr := ParseBatchRequest(map[string]any{
		"idempotency_key": idemKey,
		"operation":       "create",
		"records": []any{
			map[string]any{"values": map[string]any{ordTitle.ID: "批量A", ordQty.ID: float64(2)}},
			map[string]any{"values": map[string]any{ordTitle.ID: "批量B", ordQty.ID: float64(3)}},
		},
	})
	if berr != nil {
		t.Fatalf("ParseBatchRequest: %v", berr)
	}
	resp1, berr := ExecuteBatch(ctx, pool, wsID, ordCreated.Table.ID, actor, batchReq)
	if berr != nil {
		t.Fatalf("ExecuteBatch 1: %v", berr)
	}
	resp2, berr := ExecuteBatch(ctx, pool, wsID, ordCreated.Table.ID, actor, batchReq)
	if berr != nil {
		t.Fatalf("ExecuteBatch replay: %v", berr)
	}
	if resp1["batch_id"] != resp2["batch_id"] {
		t.Fatalf("idempotency replay returned different batch_id: %v vs %v", resp1["batch_id"], resp2["batch_id"])
	}
	diffReq, _ := ParseBatchRequest(map[string]any{
		"idempotency_key": idemKey,
		"operation":       "create",
		"records": []any{
			map[string]any{"values": map[string]any{ordTitle.ID: "不同内容"}},
		},
	})
	if _, berr := ExecuteBatch(ctx, pool, wsID, ordCreated.Table.ID, actor, diffReq); berr == nil || berr.IdempotencyConflict == "" {
		t.Fatalf("expected idempotency conflict, got %+v", berr)
	}

	// 7. Views: form view submit + grid filter/sort.
	formKind := "form"
	formView, err := CreateView(ctx, pool, wsID, ordCreated.Table.ID, ViewWrite{
		Name:    "下单表单",
		Kind:    &formKind,
		Config:  ConfigUpdate{Set: true, Value: map[string]any{"required_field_ids": []any{ordTitle.ID}}},
		Columns: []string{ordTitle.ID, ordQty.ID},
	}, &actor)
	if err != nil {
		t.Fatalf("CreateView form: %v", err)
	}
	if _, err := SubmitForm(ctx, pool, wsID, ordCreated.Table.ID, formView.ID, map[string]any{ordQty.ID: 1}, &actor); err == nil {
		t.Fatal("expected missing required field to fail form submit")
	}
	formRec, err := SubmitForm(ctx, pool, wsID, ordCreated.Table.ID, formView.ID, map[string]any{ordTitle.ID: "表单单", ordQty.ID: 5}, &actor)
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	// Delete the form-submitted record (no external references).
	if _, err := DeleteRecord(ctx, pool, wsID, ordCreated.Table.ID, formRec.ID, formRec.Revision, actor); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}

	// 8. History endpoints (table history trigger populates generic_history_revision/version).
	histList, herr := ListHistory(ctx, pool, wsID, ordCreated.Table.ID, nil, nil)
	if herr != nil {
		t.Fatalf("ListHistory: %v", herr)
	}
	if histList.LatestRevision != nil {
		snap, herr := GetHistorySnapshot(ctx, pool, wsID, ordCreated.Table.ID, SnapshotTarget{Revision: histList.LatestRevision})
		if herr != nil || snap.Table.ID != ordCreated.Table.ID {
			t.Fatalf("GetHistorySnapshot: %+v, err=%v", snap, herr)
		}
	}
	if _, herr := ListDeletedRecordsHistory(ctx, pool, wsID, ordCreated.Table.ID, nil, nil); herr != nil {
		t.Fatalf("ListDeletedRecordsHistory: %v", herr)
	}

	// 9. Portable template export & import (self-contained table).
	portCreated, terr := CreateTableWithFields(ctx, pool, wsID, "便携模板源", []TemplateFieldInput{
		{ID: strPtr("f-name"), Name: "名称", Kind: "text", Config: map[string]any{}},
		{ID: strPtr("f-val"), Name: "数值", Kind: "number", Config: map[string]any{}},
		{ID: strPtr("f-dbl"), Name: "翻倍", Kind: "formula", Config: map[string]any{"expression": "{数值} * 2"}},
	})
	if terr != nil {
		t.Fatalf("CreateTableWithFields: %v", terr)
	}
	portRec, err := CreateRecord(ctx, pool, wsID, portCreated.Table.ID, map[string]any{
		portCreated.Fields[0].ID: "样本1",
		portCreated.Fields[1].ID: 21,
	}, &actor)
	if err != nil {
		t.Fatalf("CreateRecord portable: %v", err)
	}
	exported, terr := ExportTemplate(ctx, pool, wsID, portCreated.Table.ID, []string{portRec.ID})
	if terr != nil || len(exported.SampleRecords) != 1 {
		t.Fatalf("ExportTemplate: %+v, err=%v", exported, terr)
	}
	exportedJSON, _ := marshalCompact(exported)
	var importReq TemplateImportRequest
	if err := json.Unmarshal(exportedJSON, &importReq); err != nil {
		t.Fatalf("unmarshal exported template: %v", err)
	}
	importReq.Name = "导入副本"
	imported, terr := ImportTemplate(ctx, pool, wsID, importReq, &actor)
	if terr != nil || len(imported.Records) != 1 {
		t.Fatalf("ImportTemplate: %+v, err=%v", imported, terr)
	}
	importedDblID := imported.IDMap.Fields[portCreated.Fields[2].ID]
	if got, ok := asFloat64(imported.Records[0].Values[importedDblID]); !ok || got != 42 {
		t.Fatalf("imported formula = %v, want 42", imported.Records[0].Values[importedDblID])
	}

	// 10. Issue derived properties: record_relation -> lookup -> formula.
	var relPropID, lookupPropID, formulaPropID, issueID string
	relPropCfg, _ := marshalCompactString(map[string]any{"target_table_id": projCreated.Table.ID})
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue_property (workspace_id, name, type, config)
		 VALUES ($1::uuid, '关联项目', 'record_relation', $2::jsonb)
		 RETURNING id::text`,
		wsID, relPropCfg,
	).Scan(&relPropID); err != nil {
		t.Fatalf("insert record_relation issue_property: %v", err)
	}

	lookupRaw, _ := marshalCompact(map[string]any{
		"relation_property_id": relPropID,
		"target_field_id":      projRate.ID,
	})
	compiledLookup, err := CompileIssuePropertyConfig(ctx, pool, wsID, nil, "lookup", lookupRaw)
	if err != nil {
		t.Fatalf("CompileIssuePropertyConfig lookup: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue_property (workspace_id, name, type, config)
		 VALUES ($1::uuid, '项目单价', 'lookup', $2::jsonb)
		 RETURNING id::text`,
		wsID, string(compiledLookup),
	).Scan(&lookupPropID); err != nil {
		t.Fatalf("insert lookup issue_property: %v", err)
	}

	formulaRaw, _ := marshalCompact(map[string]any{
		"expression": "SUM({项目单价}) * 10",
	})
	compiledFormula, err := CompileIssuePropertyConfig(ctx, pool, wsID, nil, "formula", formulaRaw)
	if err != nil {
		t.Fatalf("CompileIssuePropertyConfig formula: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue_property (workspace_id, name, type, config)
		 VALUES ($1::uuid, '预算估算', 'formula', $2::jsonb)
		 RETURNING id::text`,
		wsID, string(compiledFormula),
	).Scan(&formulaPropID); err != nil {
		t.Fatalf("insert formula issue_property: %v", err)
	}

	issuePropsJSON, _ := marshalCompactString(map[string]any{relPropID: []any{projRec.ID}})
	if err := pool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number, properties)
		 VALUES ($1::uuid, '预算任务', 'todo', 'none', 'member', $2::uuid, 1, $3::jsonb)
		 RETURNING id::text`,
		wsID, userID, issuePropsJSON,
	).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	projected, err := ProjectIssueProperties(ctx, pool, wsID, []string{issueID})
	if err != nil {
		t.Fatalf("ProjectIssueProperties: %v", err)
	}
	if string(projected[issueID][formulaPropID]) != "250" {
		t.Fatalf("projected formula = %s, want 250 (bag=%v)", projected[issueID][formulaPropID], projected[issueID])
	}
	singleVal, err := ProjectTaskFormula(ctx, pool, wsID, issueID, formulaPropID)
	if err != nil || string(singleVal) != "250" {
		t.Fatalf("ProjectTaskFormula = %s, err=%v", singleVal, err)
	}

	// 11. HTTP Handler end-to-end smoke across /api/tables, /api/tables/, and /api/tables/{id}.
	h := NewHandler(pool, nil)
	userUUID, _ := util.ParseUUID(userID)
	doHTTP := func(method, path string, body any) *httptest.ResponseRecorder {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), wsID, db.Member{UserID: userUUID}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := doHTTP(http.MethodGet, "/api/tables", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tables -> %d %s", rec.Code, rec.Body.String())
	}
	if rec := doHTTP(http.MethodGet, "/api/tables/", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tables/ -> %d %s", rec.Code, rec.Body.String())
	}
	if rec := doHTTP(http.MethodGet, fmt.Sprintf("/api/tables/%s", ordCreated.Table.ID), nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tables/{id} -> %d %s", rec.Code, rec.Body.String())
	}
	if rec := doHTTP(http.MethodPost, "/api/tables", map[string]any{"name": "HTTP新表"}); rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/tables -> %d %s", rec.Code, rec.Body.String())
	}
}
