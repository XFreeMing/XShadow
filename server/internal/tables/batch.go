package tables

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const MaxBatchRecords = 10

type BatchOperation string

const (
	BatchOpCreate BatchOperation = "create"
	BatchOpUpdate BatchOperation = "update"
	BatchOpDelete BatchOperation = "delete"
)

type BatchCreateItem struct {
	Values map[string]any `json:"values"`
}

type BatchUpdateItem struct {
	ID               string         `json:"id"`
	ExpectedRevision int64          `json:"expected_revision"`
	Values           map[string]any `json:"values"`
}

type BatchDeleteItem struct {
	ID               string `json:"id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type BatchRequest struct {
	IdempotencyKey string
	Operation      BatchOperation
	CreateItems    []BatchCreateItem
	UpdateItems    []BatchUpdateItem
	DeleteItems    []BatchDeleteItem
}

type BatchError struct {
	Index               *int
	RecordID            *string
	StoreErr            *StoreError
	IdempotencyConflict string
}

func (e *BatchError) Error() string {
	if e.IdempotencyConflict != "" {
		return e.IdempotencyConflict
	}
	if e.StoreErr != nil {
		return e.StoreErr.Error()
	}
	return "批量请求失败"
}

func batchTopErr(err error) *BatchError {
	return &BatchError{StoreErr: errFailed(err)}
}

func batchItemErr(idx int, recordID *string, err error) *BatchError {
	i := idx
	return &BatchError{Index: &i, RecordID: recordID, StoreErr: errFailed(err)}
}

func batchIdempotencyErr(msg string) *BatchError {
	return &BatchError{IdempotencyConflict: msg}
}

func ParseBatchRequest(raw any) (*BatchRequest, *BatchError) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, batchTopErr(errInvalid("请求格式不正确"))
	}
	for k := range obj {
		if k != "idempotency_key" && k != "operation" && k != "records" {
			return nil, batchTopErr(errInvalid("批量请求包含不支持的参数"))
		}
	}
	rawKey, ok := obj["idempotency_key"].(string)
	if !ok || !IsUUID(strings.TrimSpace(rawKey)) {
		return nil, batchTopErr(errInvalid("idempotency_key 必须是有效的 UUID"))
	}
	idempotencyKey := strings.ToLower(strings.TrimSpace(rawKey))

	rawOp, ok := obj["operation"].(string)
	if !ok {
		return nil, batchTopErr(errInvalid("operation 必须是 create、update 或 delete"))
	}
	var op BatchOperation
	switch rawOp {
	case "create":
		op = BatchOpCreate
	case "update":
		op = BatchOpUpdate
	case "delete":
		op = BatchOpDelete
	default:
		return nil, batchTopErr(errInvalid("operation 必须是 create、update 或 delete"))
	}

	records, ok := obj["records"].([]any)
	if !ok {
		return nil, batchTopErr(errInvalid("records 必须是非空数组"))
	}
	if len(records) == 0 || len(records) > MaxBatchRecords {
		return nil, batchTopErr(errInvalid("批量记录数量必须在 1 到 10 条之间"))
	}

	req := &BatchRequest{
		IdempotencyKey: idempotencyKey,
		Operation:      op,
	}
	switch op {
	case BatchOpCreate:
		req.CreateItems = make([]BatchCreateItem, 0, len(records))
		for idx, entry := range records {
			item, berr := parseBatchCreateItem(idx, entry)
			if berr != nil {
				return nil, berr
			}
			req.CreateItems = append(req.CreateItems, item)
		}
	case BatchOpUpdate:
		seen := make(map[string]struct{}, len(records))
		req.UpdateItems = make([]BatchUpdateItem, 0, len(records))
		for idx, entry := range records {
			item, berr := parseBatchUpdateItem(idx, entry)
			if berr != nil {
				return nil, berr
			}
			if _, dup := seen[item.ID]; dup {
				id := item.ID
				return nil, batchItemErr(idx, &id, errInvalid("批量请求中存在重复的记录标识"))
			}
			seen[item.ID] = struct{}{}
			req.UpdateItems = append(req.UpdateItems, item)
		}
	case BatchOpDelete:
		seen := make(map[string]struct{}, len(records))
		req.DeleteItems = make([]BatchDeleteItem, 0, len(records))
		for idx, entry := range records {
			item, berr := parseBatchDeleteItem(idx, entry)
			if berr != nil {
				return nil, berr
			}
			if _, dup := seen[item.ID]; dup {
				id := item.ID
				return nil, batchItemErr(idx, &id, errInvalid("批量请求中存在重复的记录标识"))
			}
			seen[item.ID] = struct{}{}
			req.DeleteItems = append(req.DeleteItems, item)
		}
	}
	return req, nil
}

func (r *BatchRequest) CanonicalRequestBytes() ([]byte, error) {
	var records any
	switch r.Operation {
	case BatchOpCreate:
		records = r.CreateItems
	case BatchOpUpdate:
		records = r.UpdateItems
	case BatchOpDelete:
		records = r.DeleteItems
	}
	// Map keys are serialized alphabetically by encoding/json: "operation", "records",
	// matching Rust serde_json::json!({"operation": ..., "records": ...}).
	canonical := map[string]any{
		"operation": string(r.Operation),
		"records":   records,
	}
	return marshalCompact(canonical)
}

func parseBatchCreateItem(idx int, entry any) (BatchCreateItem, *BatchError) {
	obj, ok := entry.(map[string]any)
	if !ok {
		return BatchCreateItem{}, batchItemErr(idx, nil, errInvalid("批量记录项必须是 JSON 对象"))
	}
	for k := range obj {
		if k != "values" {
			return BatchCreateItem{}, batchItemErr(idx, nil, errInvalid("创建记录项包含不支持的参数"))
		}
	}
	rawValues, exists := obj["values"]
	if !exists {
		return BatchCreateItem{Values: map[string]any{}}, nil
	}
	values, berr := parseBatchValuesMap(idx, nil, rawValues, false)
	if berr != nil {
		return BatchCreateItem{}, berr
	}
	return BatchCreateItem{Values: values}, nil
}

func parseBatchUpdateItem(idx int, entry any) (BatchUpdateItem, *BatchError) {
	obj, ok := entry.(map[string]any)
	if !ok {
		return BatchUpdateItem{}, batchItemErr(idx, nil, errInvalid("批量记录项必须是 JSON 对象"))
	}
	for k := range obj {
		if k != "id" && k != "expected_revision" && k != "values" {
			return BatchUpdateItem{}, batchItemErr(idx, nil, errInvalid("更新记录项包含不支持的参数"))
		}
	}
	id, berr := parseBatchRecordID(idx, obj)
	if berr != nil {
		return BatchUpdateItem{}, berr
	}
	rev, berr := parseBatchExpectedRevision(idx, id, obj)
	if berr != nil {
		return BatchUpdateItem{}, berr
	}
	rawValues, exists := obj["values"]
	if !exists {
		idCopy := id
		return BatchUpdateItem{}, batchItemErr(idx, &idCopy, errInvalid("没有要保存的字段"))
	}
	values, berr := parseBatchValuesMap(idx, &id, rawValues, true)
	if berr != nil {
		return BatchUpdateItem{}, berr
	}
	return BatchUpdateItem{ID: id, ExpectedRevision: rev, Values: values}, nil
}

func parseBatchDeleteItem(idx int, entry any) (BatchDeleteItem, *BatchError) {
	obj, ok := entry.(map[string]any)
	if !ok {
		return BatchDeleteItem{}, batchItemErr(idx, nil, errInvalid("批量记录项必须是 JSON 对象"))
	}
	for k := range obj {
		if k != "id" && k != "expected_revision" {
			return BatchDeleteItem{}, batchItemErr(idx, nil, errInvalid("删除记录项包含不支持的参数"))
		}
	}
	id, berr := parseBatchRecordID(idx, obj)
	if berr != nil {
		return BatchDeleteItem{}, berr
	}
	rev, berr := parseBatchExpectedRevision(idx, id, obj)
	if berr != nil {
		return BatchDeleteItem{}, berr
	}
	return BatchDeleteItem{ID: id, ExpectedRevision: rev}, nil
}

func parseBatchRecordID(idx int, obj map[string]any) (string, *BatchError) {
	raw, ok := obj["id"].(string)
	if !ok || !IsUUID(strings.TrimSpace(raw)) {
		return "", batchItemErr(idx, nil, errInvalid("记录标识无效"))
	}
	return strings.ToLower(strings.TrimSpace(raw)), nil
}

func parseBatchExpectedRevision(idx int, recordID string, obj map[string]any) (int64, *BatchError) {
	idCopy := recordID
	raw, exists := obj["expected_revision"]
	if !exists || raw == nil {
		return 0, batchItemErr(idx, &idCopy, errInvalid("记录版本号无效"))
	}
	rev, ok := parseExactInt64(raw)
	if !ok || rev <= 0 {
		return 0, batchItemErr(idx, &idCopy, errInvalid("记录版本号无效"))
	}
	return rev, nil
}

func parseExactInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func parseBatchValuesMap(idx int, recordID *string, raw any, requireNonEmpty bool) (map[string]any, *BatchError) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, batchItemErr(idx, recordID, errInvalid("values 必须是 JSON 对象"))
	}
	if requireNonEmpty && len(m) == 0 {
		return nil, batchItemErr(idx, recordID, errInvalid("没有要保存的字段"))
	}
	out := make(map[string]any, len(m))
	for fieldID, val := range m {
		if !IsUUID(fieldID) {
			return nil, batchItemErr(idx, recordID, errInvalid("字段标识无效"))
		}
		out[strings.ToLower(fieldID)] = val
	}
	return out, nil
}

func ExecuteBatch(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	actor Actor,
	req *BatchRequest,
) (map[string]any, *BatchError) {
	tableLower := strings.ToLower(tableID)
	canonicalBytes, err := req.CanonicalRequestBytes()
	if err != nil {
		return nil, batchTopErr(err)
	}
	sum := sha256.Sum256(canonicalBytes)
	requestHash := hex.EncodeToString(sum[:])
	opStr := string(req.Operation)

	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, batchTopErr(err)
	}
	defer tx.Rollback(ctx)

	var lockedTables []string
	switch req.Operation {
	case BatchOpCreate:
		touched := collectBatchTouchedCreate(req.CreateItems)
		lockedTables, err = prepareTableLocks(ctx, tx, workspace, tableLower, touched)
		if err != nil {
			return nil, batchTopErr(err)
		}
	case BatchOpUpdate:
		touched := collectBatchTouchedUpdate(req.UpdateItems)
		lockedTables, err = prepareTableLocks(ctx, tx, workspace, tableLower, touched)
		if err != nil {
			return nil, batchTopErr(err)
		}
	case BatchOpDelete:
		if err := lockWorkspaceTablesSorted(ctx, tx, workspace, tableLower); err != nil {
			return nil, batchTopErr(err)
		}
	}

	replayed, berr := checkBatchIdempotency(ctx, tx, workspace, tableLower, actor, req.IdempotencyKey, opStr, requestHash)
	if berr != nil {
		return nil, berr
	}
	if replayed != nil {
		if err := commitTx(ctx, tx); err != nil {
			return nil, batchTopErr(err)
		}
		return replayed, nil
	}

	var batchID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&batchID); err != nil {
		return nil, batchTopErr(logFailed(err))
	}

	var response map[string]any
	switch req.Operation {
	case BatchOpCreate:
		fields, err := loadFields(ctx, tx, workspace, tableLower)
		if err != nil {
			return nil, batchTopErr(err)
		}
		results := make([]RecordDto, 0, len(req.CreateItems))
		for idx, item := range req.CreateItems {
			normalized, err := normalizeValues(fields, item.Values)
			if err != nil {
				return nil, batchItemErr(idx, nil, err)
			}
			created, err := createRecordInTx(ctx, tx, workspace, tableLower, fields, normalized, lockedTables, &actor, &batchID)
			if err != nil {
				return nil, batchItemErr(idx, nil, err)
			}
			results = append(results, created)
		}
		response = map[string]any{
			"operation":       opStr,
			"idempotency_key": req.IdempotencyKey,
			"batch_id":        batchID,
			"records":         results,
		}
	case BatchOpUpdate:
		fields, err := loadFields(ctx, tx, workspace, tableLower)
		if err != nil {
			return nil, batchTopErr(err)
		}
		results := make([]RecordDto, 0, len(req.UpdateItems))
		for idx, item := range req.UpdateItems {
			updated, err := updateRecordInTx(ctx, tx, workspace, tableLower, fields, lockedTables, item.ID, item.ExpectedRevision, item.Values, actor, &batchID)
			if err != nil {
				idCopy := item.ID
				return nil, batchItemErr(idx, &idCopy, err)
			}
			results = append(results, updated)
		}
		response = map[string]any{
			"operation":       opStr,
			"idempotency_key": req.IdempotencyKey,
			"batch_id":        batchID,
			"records":         results,
		}
	case BatchOpDelete:
		results := make([]DeletedDto, 0, len(req.DeleteItems))
		for idx, item := range req.DeleteItems {
			deleted, err := deleteRecordInTx(ctx, tx, workspace, tableLower, item.ID, item.ExpectedRevision, actor, &batchID)
			if err != nil {
				idCopy := item.ID
				return nil, batchItemErr(idx, &idCopy, err)
			}
			results = append(results, deleted)
		}
		response = map[string]any{
			"operation":       opStr,
			"idempotency_key": req.IdempotencyKey,
			"batch_id":        batchID,
			"records":         results,
		}
	}

	encodedResp, err := marshalCompactString(response)
	if err != nil {
		return nil, batchTopErr(errFailed(err))
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO generic_record_batch_idempotency (
		    workspace_id, table_id, actor_source, actor_id, idempotency_key,
		    operation, request_hash, batch_id, response_json
		 ) VALUES (
		    $1::uuid, $2::uuid, $3, $4::uuid, $5::uuid,
		    $6, $7, $8::uuid, $9::jsonb
		 )`,
		workspace, tableLower, actor.Source, actor.ID, req.IdempotencyKey,
		opStr, requestHash, batchID, encodedResp,
	); err != nil {
		return nil, batchTopErr(logFailed(err))
	}

	if err := commitTx(ctx, tx); err != nil {
		return nil, batchTopErr(err)
	}
	return response, nil
}

func collectBatchTouchedCreate(items []BatchCreateItem) []string {
	set := make(map[string]struct{})
	for _, item := range items {
		for k := range item.Values {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func collectBatchTouchedUpdate(items []BatchUpdateItem) []string {
	set := make(map[string]struct{})
	for _, item := range items {
		for k := range item.Values {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkBatchIdempotency(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID string,
	actor Actor,
	idempotencyKey, operation, requestHash string,
) (map[string]any, *BatchError) {
	var storedOp, storedHash, storedResp string
	err := tx.QueryRow(ctx,
		`SELECT operation, request_hash, response_json::text
		 FROM generic_record_batch_idempotency
		 WHERE workspace_id = $1::uuid
		   AND table_id = $2::uuid
		   AND actor_source = $3
		   AND actor_id = $4::uuid
		   AND idempotency_key = $5::uuid`,
		workspace, tableID, actor.Source, actor.ID, idempotencyKey,
	).Scan(&storedOp, &storedHash, &storedResp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, batchTopErr(logFailed(err))
	}
	if storedOp != operation || storedHash != requestHash {
		return nil, batchIdempotencyErr("同一个 idempotency_key 不能用于不同的批量请求内容")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(storedResp), &parsed); err != nil {
		return nil, batchTopErr(errFailed(err))
	}
	return parsed, nil
}
