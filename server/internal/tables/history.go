package tables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 200
	computedValuesMode  = "stored_raw_only"
)

type HistoryErrorKind int

const (
	HistoryErrFailed HistoryErrorKind = iota
	HistoryErrNotFound
	HistoryErrNotAvailable
	HistoryErrInvalid
)

type HistoryError struct {
	Kind    HistoryErrorKind
	Message string
}

func (e *HistoryError) Error() string { return e.Message }

func histNotFound(msg string) *HistoryError     { return &HistoryError{Kind: HistoryErrNotFound, Message: msg} }
func histNotAvailable(msg string) *HistoryError { return &HistoryError{Kind: HistoryErrNotAvailable, Message: msg} }
func histInvalid(msg string) *HistoryError      { return &HistoryError{Kind: HistoryErrInvalid, Message: msg} }
func histFailed(err error) *HistoryError {
	if err != nil {
		var he *HistoryError
		if errors.As(err, &he) {
			return he
		}
		var se *StoreError
		if errors.As(err, &se) {
			switch se.Kind {
			case ErrKindNotFound:
				return histNotFound(se.Message)
			case ErrKindInvalid:
				return histInvalid(se.Message)
			}
		}
		_ = logFailed(err)
	}
	return &HistoryError{Kind: HistoryErrFailed, Message: "无法读取表格历史"}
}

type SnapshotTarget struct {
	Revision  *int64
	Timestamp *string
}

type HistorySummaryDto struct {
	TableChanges  int64 `json:"table_changes"`
	FieldChanges  int64 `json:"field_changes"`
	ViewChanges   int64 `json:"view_changes"`
	RecordChanges int64 `json:"record_changes"`
	CellChanges   int64 `json:"cell_changes"`
	TotalChanges  int64 `json:"total_changes"`
}

type HistoryRevisionDto struct {
	Revision   int64             `json:"revision"`
	RecordedAt string            `json:"recorded_at"`
	Baseline   bool              `json:"baseline"`
	Summary    HistorySummaryDto `json:"summary"`
}

type HistoryListDto struct {
	TableID          string               `json:"table_id"`
	BaselineRevision *int64               `json:"baseline_revision"`
	LatestRevision   *int64               `json:"latest_revision"`
	Revisions        []HistoryRevisionDto `json:"revisions"`
	Limit            int64                `json:"limit"`
	HasMore          bool                 `json:"has_more"`
	NextCursor       *string              `json:"next_cursor"`
}

type HistorySnapshotDto struct {
	Revision           int64       `json:"revision"`
	RecordedAt         string      `json:"recorded_at"`
	Baseline           bool        `json:"baseline"`
	ComputedValuesMode string      `json:"computed_values_mode"`
	Table              TableDto    `json:"table"`
	Fields             []FieldDto  `json:"fields"`
	Views              []ViewDto   `json:"views"`
	Records            []RecordDto `json:"records"`
}

type DeletedRecordHistoryDto struct {
	RecordID          string `json:"record_id"`
	DeletedAtRevision int64  `json:"deleted_at_revision"`
	DeletedAt         string `json:"deleted_at"`
}

type HistoryDeletedListDto struct {
	TableID        string                    `json:"table_id"`
	DeletedRecords []DeletedRecordHistoryDto `json:"deleted_records"`
	Limit          int64                     `json:"limit"`
	HasMore        bool                      `json:"has_more"`
	NextCursor     *string                   `json:"next_cursor"`
}

type deletedCursor struct {
	revision int64
	recordID *string
}

func ParseHistoryLimit(limitOpt *int64) (int64, *HistoryError) {
	if limitOpt == nil {
		return defaultHistoryLimit, nil
	}
	n := *limitOpt
	if n < 1 || n > maxHistoryLimit {
		return 0, histInvalid("limit 必须在 1 到 200 之间")
	}
	return n, nil
}

func ParseRevisionCursor(cursor *string) (*int64, *HistoryError) {
	if cursor == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(*cursor)
	if raw == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, histInvalid("cursor 无效")
	}
	return &parsed, nil
}

func parseDeletedCursor(cursor *string) (*deletedCursor, *HistoryError) {
	if cursor == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(*cursor)
	if raw == "" {
		return nil, nil
	}
	if revPart, idPart, ok := strings.Cut(raw, ":"); ok {
		rev, err := strconv.ParseInt(revPart, 10, 64)
		if err != nil || rev <= 0 || !IsUUID(idPart) {
			return nil, histInvalid("cursor 无效")
		}
		idCopy := idPart
		return &deletedCursor{revision: rev, recordID: &idCopy}, nil
	}
	rev, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || rev <= 0 {
		return nil, histInvalid("cursor 无效")
	}
	return &deletedCursor{revision: rev}, nil
}

func ParseSnapshotTarget(revision *int64, timestamp *string) (SnapshotTarget, *HistoryError) {
	var tsClean *string
	if timestamp != nil {
		trimmed := strings.TrimSpace(*timestamp)
		if trimmed != "" {
			tsClean = &trimmed
		}
	}
	switch {
	case revision != nil && tsClean == nil:
		if *revision <= 0 {
			return SnapshotTarget{}, histInvalid("revision 必须是正整数")
		}
		rev := *revision
		return SnapshotTarget{Revision: &rev}, nil
	case revision == nil && tsClean != nil:
		parsed, err := time.Parse(time.RFC3339Nano, *tsClean)
		if err != nil {
			return SnapshotTarget{}, histInvalid("timestamp 必须是有效的 ISO-8601 时间")
		}
		formatted := parsed.UTC().Format("2006-01-02T15:04:05.000000Z")
		return SnapshotTarget{Timestamp: &formatted}, nil
	case revision != nil && tsClean != nil:
		return SnapshotTarget{}, histInvalid("revision 和 timestamp 不能同时提供")
	default:
		return SnapshotTarget{}, histInvalid("必须提供 revision 或 timestamp")
	}
}

func beginRepeatableReadOnly(ctx context.Context, pool Pool) (pgx.Tx, *HistoryError) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, histFailed(err)
	}
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, histFailed(err)
	}
	return tx, nil
}

func revisionBounds(ctx context.Context, tx pgx.Tx, workspace, tableID string) (*int64, *int64, *HistoryError) {
	var minRev, maxRev *int64
	if err := tx.QueryRow(ctx,
		`SELECT MIN(revision), MAX(revision)
		 FROM generic_history_revision
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		workspace, tableID,
	).Scan(&minRev, &maxRev); err != nil {
		return nil, nil, histFailed(err)
	}
	return minRev, maxRev, nil
}

func ListHistory(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	limitOpt *int64,
	cursor *string,
) (HistoryListDto, *HistoryError) {
	limit, herr := ParseHistoryLimit(limitOpt)
	if herr != nil {
		return HistoryListDto{}, herr
	}
	cursorRev, herr := ParseRevisionCursor(cursor)
	if herr != nil {
		return HistoryListDto{}, herr
	}

	tx, herr := beginRepeatableReadOnly(ctx, pool)
	if herr != nil {
		return HistoryListDto{}, herr
	}
	defer tx.Rollback(ctx)
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return HistoryListDto{}, histFailed(err)
	}
	baselineRev, latestRev, herr := revisionBounds(ctx, tx, workspace, tableID)
	if herr != nil {
		return HistoryListDto{}, herr
	}

	recorded := historyTSExpr("r.recorded_at")
	sql := fmt.Sprintf(
		`SELECT r.revision, %s, r.baseline,
		        COUNT(*) FILTER (WHERE v.kind = 'table')::bigint,
		        COUNT(*) FILTER (WHERE v.kind = 'field')::bigint,
		        COUNT(*) FILTER (WHERE v.kind = 'view')::bigint,
		        COUNT(*) FILTER (WHERE v.kind = 'record')::bigint,
		        COUNT(*) FILTER (WHERE v.kind = 'cell')::bigint,
		        COUNT(v.revision)::bigint
		 FROM generic_history_revision r
		 LEFT JOIN generic_history_version v ON v.revision = r.revision
		 WHERE r.workspace_id = $1::uuid
		   AND r.table_id = $2::uuid
		   AND ($3::bigint IS NULL OR r.revision < $3)
		 GROUP BY r.revision, r.recorded_at, r.baseline
		 ORDER BY r.revision DESC
		 LIMIT $4`, recorded)
	rows, err := tx.Query(ctx, sql, workspace, tableID, cursorRev, limit+1)
	if err != nil {
		return HistoryListDto{}, histFailed(err)
	}
	all := make([]HistoryRevisionDto, 0)
	for rows.Next() {
		var r HistoryRevisionDto
		if err := rows.Scan(
			&r.Revision, &r.RecordedAt, &r.Baseline,
			&r.Summary.TableChanges, &r.Summary.FieldChanges, &r.Summary.ViewChanges,
			&r.Summary.RecordChanges, &r.Summary.CellChanges, &r.Summary.TotalChanges,
		); err != nil {
			rows.Close()
			return HistoryListDto{}, histFailed(err)
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return HistoryListDto{}, histFailed(err)
	}
	if err := commitTx(ctx, tx); err != nil {
		return HistoryListDto{}, histFailed(err)
	}

	hasMore := int64(len(all)) > limit
	if hasMore {
		all = all[:limit]
	}
	var nextCursor *string
	if hasMore && len(all) > 0 {
		s := strconv.FormatInt(all[len(all)-1].Revision, 10)
		nextCursor = &s
	}

	return HistoryListDto{
		TableID:          tableID,
		BaselineRevision: baselineRev,
		LatestRevision:   latestRev,
		Revisions:        all,
		Limit:            limit,
		HasMore:          hasMore,
		NextCursor:       nextCursor,
	}, nil
}

func GetHistorySnapshot(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	target SnapshotTarget,
) (HistorySnapshotDto, *HistoryError) {
	tx, herr := beginRepeatableReadOnly(ctx, pool)
	if herr != nil {
		return HistorySnapshotDto{}, herr
	}
	defer tx.Rollback(ctx)
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return HistorySnapshotDto{}, histFailed(err)
	}
	minRev, maxRev, herr := revisionBounds(ctx, tx, workspace, tableID)
	if herr != nil {
		return HistorySnapshotDto{}, herr
	}
	if minRev == nil || maxRev == nil {
		return HistorySnapshotDto{}, histNotAvailable("该表尚无可用历史基线")
	}

	recorded := historyTSExpr("recorded_at")
	var resolvedRev int64
	var recordedAt string
	var baseline bool
	var queryErr error
	if target.Revision != nil {
		rev := *target.Revision
		if rev < *minRev || rev > *maxRev {
			return HistorySnapshotDto{}, histNotAvailable("请求的修订版本不在该表的可用历史范围内")
		}
		sql := fmt.Sprintf(
			`SELECT revision, %s, baseline
			 FROM generic_history_revision
			 WHERE workspace_id = $1::uuid
			   AND table_id = $2::uuid
			   AND revision = $3
			 LIMIT 1`, recorded)
		queryErr = tx.QueryRow(ctx, sql, workspace, tableID, rev).Scan(&resolvedRev, &recordedAt, &baseline)
	} else {
		sql := fmt.Sprintf(
			`SELECT revision, %s, baseline
			 FROM generic_history_revision
			 WHERE workspace_id = $1::uuid
			   AND table_id = $2::uuid
			   AND recorded_at <= $3::text::timestamptz
			 ORDER BY revision DESC
			 LIMIT 1`, recorded)
		queryErr = tx.QueryRow(ctx, sql, workspace, tableID, *target.Timestamp).Scan(&resolvedRev, &recordedAt, &baseline)
	}
	if errors.Is(queryErr, pgx.ErrNoRows) {
		return HistorySnapshotDto{}, histNotAvailable("请求的修订版本不存在或时间早于该表历史基线")
	}
	if queryErr != nil {
		return HistorySnapshotDto{}, histFailed(queryErr)
	}

	rows, err := tx.Query(ctx,
		`SELECT DISTINCT ON (v.kind, v.entity_id, v.secondary_id)
		        v.kind,
		        v.entity_id::text,
		        v.secondary_id::text,
		        v.payload::text
		 FROM generic_history_version v
		 JOIN generic_history_revision r ON r.revision = v.revision
		 WHERE r.workspace_id = $1::uuid
		   AND r.table_id = $2::uuid
		   AND r.revision <= $3
		 ORDER BY v.kind, v.entity_id, v.secondary_id, v.revision DESC`,
		workspace, tableID, resolvedRev,
	)
	if err != nil {
		return HistorySnapshotDto{}, histFailed(err)
	}
	var versions []versionEntry
	for rows.Next() {
		var kind, entityID, secondaryID string
		var payloadText *string
		if err := rows.Scan(&kind, &entityID, &secondaryID, &payloadText); err != nil {
			rows.Close()
			return HistorySnapshotDto{}, histFailed(err)
		}
		var payload map[string]any
		hasPayload := false
		if payloadText != nil {
			if err := json.Unmarshal([]byte(*payloadText), &payload); err != nil {
				rows.Close()
				return HistorySnapshotDto{}, histFailed(err)
			}
			hasPayload = true
		}
		versions = append(versions, versionEntry{
			kind:        kind,
			entityID:    entityID,
			secondaryID: secondaryID,
			payload:     payload,
			hasPayload:  hasPayload,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return HistorySnapshotDto{}, histFailed(err)
	}
	if err := commitTx(ctx, tx); err != nil {
		return HistorySnapshotDto{}, histFailed(err)
	}

	return assembleHistorySnapshot(resolvedRev, recordedAt, baseline, versions)
}

func ListDeletedRecordsHistory(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	limitOpt *int64,
	cursor *string,
) (HistoryDeletedListDto, *HistoryError) {
	limit, herr := ParseHistoryLimit(limitOpt)
	if herr != nil {
		return HistoryDeletedListDto{}, herr
	}
	parsedCursor, herr := parseDeletedCursor(cursor)
	if herr != nil {
		return HistoryDeletedListDto{}, herr
	}

	tx, herr := beginRepeatableReadOnly(ctx, pool)
	if herr != nil {
		return HistoryDeletedListDto{}, herr
	}
	defer tx.Rollback(ctx)
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return HistoryDeletedListDto{}, histFailed(err)
	}

	recorded := historyTSExpr("r.recorded_at")
	fetchLimit := limit + 1
	var rows pgx.Rows
	var err error
	switch {
	case parsedCursor == nil:
		sql := fmt.Sprintf(
			`SELECT v.entity_id::text, v.revision, %s
			 FROM generic_history_version v
			 JOIN generic_history_revision r ON r.revision = v.revision
			 WHERE r.workspace_id = $1::uuid
			   AND r.table_id = $2::uuid
			   AND v.kind = 'record'
			   AND v.payload IS NULL
			 ORDER BY v.revision DESC, v.entity_id DESC
			 LIMIT $3`, recorded)
		rows, err = tx.Query(ctx, sql, workspace, tableID, fetchLimit)
	case parsedCursor.recordID == nil:
		sql := fmt.Sprintf(
			`SELECT v.entity_id::text, v.revision, %s
			 FROM generic_history_version v
			 JOIN generic_history_revision r ON r.revision = v.revision
			 WHERE r.workspace_id = $1::uuid
			   AND r.table_id = $2::uuid
			   AND v.kind = 'record'
			   AND v.payload IS NULL
			   AND v.revision < $3
			 ORDER BY v.revision DESC, v.entity_id DESC
			 LIMIT $4`, recorded)
		rows, err = tx.Query(ctx, sql, workspace, tableID, parsedCursor.revision, fetchLimit)
	default:
		sql := fmt.Sprintf(
			`SELECT v.entity_id::text, v.revision, %s
			 FROM generic_history_version v
			 JOIN generic_history_revision r ON r.revision = v.revision
			 WHERE r.workspace_id = $1::uuid
			   AND r.table_id = $2::uuid
			   AND v.kind = 'record'
			   AND v.payload IS NULL
			   AND (v.revision, v.entity_id) < ($3, $4::uuid)
			 ORDER BY v.revision DESC, v.entity_id DESC
			 LIMIT $5`, recorded)
		rows, err = tx.Query(ctx, sql, workspace, tableID, parsedCursor.revision, *parsedCursor.recordID, fetchLimit)
	}
	if err != nil {
		return HistoryDeletedListDto{}, histFailed(err)
	}
	all := make([]DeletedRecordHistoryDto, 0)
	for rows.Next() {
		var d DeletedRecordHistoryDto
		if err := rows.Scan(&d.RecordID, &d.DeletedAtRevision, &d.DeletedAt); err != nil {
			rows.Close()
			return HistoryDeletedListDto{}, histFailed(err)
		}
		all = append(all, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return HistoryDeletedListDto{}, histFailed(err)
	}
	if err := commitTx(ctx, tx); err != nil {
		return HistoryDeletedListDto{}, histFailed(err)
	}

	hasMore := int64(len(all)) > limit
	if hasMore {
		all = all[:limit]
	}
	var nextCursor *string
	if hasMore && len(all) > 0 {
		last := all[len(all)-1]
		s := fmt.Sprintf("%d:%s", last.DeletedAtRevision, last.RecordID)
		nextCursor = &s
	}
	return HistoryDeletedListDto{
		TableID:        tableID,
		DeletedRecords: all,
		Limit:          limit,
		HasMore:        hasMore,
		NextCursor:     nextCursor,
	}, nil
}

type versionEntry struct {
	kind        string
	entityID    string
	secondaryID string
	payload     map[string]any
	hasPayload  bool
}

func assembleHistorySnapshot(
	revision int64,
	recordedAt string,
	baseline bool,
	versions []versionEntry,
) (HistorySnapshotDto, *HistoryError) {
	var table *TableDto
	fields := make([]FieldDto, 0)
	type viewWithRaw struct {
		rawCreated string
		view       ViewDto
	}
	var viewsRaw []viewWithRaw
	type recWithRaw struct {
		rawCreated string
		rec        RecordDto
	}
	var recsRaw []recWithRaw
	cellsByRecord := make(map[string]map[string]any)

	for _, entry := range versions {
		if !entry.hasPayload || entry.payload == nil {
			continue
		}
		switch entry.kind {
		case "table":
			t, herr := parseHistoryTablePayload(entry.payload)
			if herr != nil {
				return HistorySnapshotDto{}, herr
			}
			table = &t
		case "field":
			f, herr := parseHistoryFieldPayload(entry.payload)
			if herr != nil {
				return HistorySnapshotDto{}, herr
			}
			fields = append(fields, f)
		case "view":
			rawCreated, herr := payloadStr(entry.payload, "created_at")
			if herr != nil {
				return HistorySnapshotDto{}, herr
			}
			v, herr := parseHistoryViewPayload(entry.payload)
			if herr != nil {
				return HistorySnapshotDto{}, herr
			}
			viewsRaw = append(viewsRaw, viewWithRaw{rawCreated: rawCreated, view: v})
		case "record":
			rawCreated, herr := payloadStr(entry.payload, "created_at")
			if herr != nil {
				return HistorySnapshotDto{}, herr
			}
			r, herr := parseHistoryRecordPayload(entry.payload)
			if herr != nil {
				return HistorySnapshotDto{}, herr
			}
			recsRaw = append(recsRaw, recWithRaw{rawCreated: rawCreated, rec: r})
		case "cell":
			val := entry.payload["value"]
			m := cellsByRecord[entry.entityID]
			if m == nil {
				m = make(map[string]any)
				cellsByRecord[entry.entityID] = m
			}
			m[entry.secondaryID] = val
		}
	}

	if table == nil {
		return HistorySnapshotDto{}, histNotAvailable("该修订版本中不存在表元数据")
	}

	sort.Slice(fields, func(i, j int) bool {
		if fields[i].Position != fields[j].Position {
			return fields[i].Position < fields[j].Position
		}
		return fields[i].ID < fields[j].ID
	})
	sort.Slice(viewsRaw, func(i, j int) bool {
		if viewsRaw[i].rawCreated != viewsRaw[j].rawCreated {
			return viewsRaw[i].rawCreated < viewsRaw[j].rawCreated
		}
		return viewsRaw[i].view.ID < viewsRaw[j].view.ID
	})
	sort.Slice(recsRaw, func(i, j int) bool {
		if recsRaw[i].rawCreated != recsRaw[j].rawCreated {
			return recsRaw[i].rawCreated < recsRaw[j].rawCreated
		}
		return recsRaw[i].rec.ID < recsRaw[j].rec.ID
	})

	views := make([]ViewDto, len(viewsRaw))
	for i, vw := range viewsRaw {
		views[i] = vw.view
	}
	records := make([]RecordDto, len(recsRaw))
	for i, rw := range recsRaw {
		rec := rw.rec
		cellMap := cellsByRecord[rec.ID]
		vals := make(map[string]any, len(fields))
		for _, f := range fields {
			if cellMap != nil {
				if v, ok := cellMap[f.ID]; ok {
					vals[f.ID] = v
					continue
				}
			}
			vals[f.ID] = nil
		}
		rec.Values = vals
		records[i] = rec
	}

	return HistorySnapshotDto{
		Revision:           revision,
		RecordedAt:         recordedAt,
		Baseline:           baseline,
		ComputedValuesMode: computedValuesMode,
		Table:              *table,
		Fields:             fields,
		Views:              views,
		Records:            records,
	}, nil
}

func payloadStr(payload map[string]any, key string) (string, *HistoryError) {
	s, ok := payload[key].(string)
	if !ok {
		return "", histFailed(nil)
	}
	return s, nil
}

func normalizeHistoryTimestamp(raw string) (string, *HistoryError) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", histFailed(err)
	}
	return parsed.UTC().Format("2006-01-02T15:04:05Z"), nil
}

func parseHistoryTablePayload(payload map[string]any) (TableDto, *HistoryError) {
	id, herr := payloadStr(payload, "id")
	if herr != nil {
		return TableDto{}, herr
	}
	name, herr := payloadStr(payload, "name")
	if herr != nil {
		return TableDto{}, herr
	}
	cRaw, herr := payloadStr(payload, "created_at")
	if herr != nil {
		return TableDto{}, herr
	}
	uRaw, herr := payloadStr(payload, "updated_at")
	if herr != nil {
		return TableDto{}, herr
	}
	createdAt, herr := normalizeHistoryTimestamp(cRaw)
	if herr != nil {
		return TableDto{}, herr
	}
	updatedAt, herr := normalizeHistoryTimestamp(uRaw)
	if herr != nil {
		return TableDto{}, herr
	}
	return TableDto{ID: id, Name: name, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func parseHistoryFieldPayload(payload map[string]any) (FieldDto, *HistoryError) {
	id, herr := payloadStr(payload, "id")
	if herr != nil {
		return FieldDto{}, herr
	}
	name, herr := payloadStr(payload, "name")
	if herr != nil {
		return FieldDto{}, herr
	}
	kind, herr := payloadStr(payload, "kind")
	if herr != nil {
		return FieldDto{}, herr
	}
	posInt, ok := parseExactInt64(payload["position"])
	if !ok {
		return FieldDto{}, histFailed(nil)
	}
	cRaw, herr := payloadStr(payload, "created_at")
	if herr != nil {
		return FieldDto{}, herr
	}
	uRaw, herr := payloadStr(payload, "updated_at")
	if herr != nil {
		return FieldDto{}, herr
	}
	createdAt, herr := normalizeHistoryTimestamp(cRaw)
	if herr != nil {
		return FieldDto{}, herr
	}
	updatedAt, herr := normalizeHistoryTimestamp(uRaw)
	if herr != nil {
		return FieldDto{}, herr
	}
	cfg := payload["config"]
	if cfg == nil {
		cfg = map[string]any{}
	}
	return FieldDto{
		ID:        id,
		Name:      name,
		Kind:      kind,
		Config:    cfg,
		Position:  int32(posInt),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func parseHistoryViewPayload(payload map[string]any) (ViewDto, *HistoryError) {
	id, herr := payloadStr(payload, "id")
	if herr != nil {
		return ViewDto{}, herr
	}
	name, herr := payloadStr(payload, "name")
	if herr != nil {
		return ViewDto{}, herr
	}
	kind := "grid"
	if k, ok := payload["kind"].(string); ok {
		kind = k
	}
	cfg := payload["config"]
	if cfg == nil {
		cfg = map[string]any{}
	}
	var cols []string
	if rawCols := payload["columns"]; rawCols != nil {
		b, err := json.Marshal(rawCols)
		if err != nil || json.Unmarshal(b, &cols) != nil {
			return ViewDto{}, histFailed(err)
		}
	}
	var filter *StoredFilter
	if rawFilter := payload["filter"]; rawFilter != nil {
		b, err := json.Marshal(rawFilter)
		if err != nil || json.Unmarshal(b, &filter) != nil {
			return ViewDto{}, histFailed(err)
		}
	}
	var sortSpec *StoredSort
	if rawSort := payload["sort"]; rawSort != nil {
		b, err := json.Marshal(rawSort)
		if err != nil || json.Unmarshal(b, &sortSpec) != nil {
			return ViewDto{}, histFailed(err)
		}
	}
	var group *string
	if g, ok := payload["group_field"].(string); ok {
		group = &g
	}
	cRaw, herr := payloadStr(payload, "created_at")
	if herr != nil {
		return ViewDto{}, herr
	}
	uRaw, herr := payloadStr(payload, "updated_at")
	if herr != nil {
		return ViewDto{}, herr
	}
	createdAt, herr := normalizeHistoryTimestamp(cRaw)
	if herr != nil {
		return ViewDto{}, herr
	}
	updatedAt, herr := normalizeHistoryTimestamp(uRaw)
	if herr != nil {
		return ViewDto{}, herr
	}
	return ViewDto{
		ID:        id,
		Name:      name,
		Kind:      kind,
		Config:    cfg,
		Columns:   cols,
		Filter:    filter,
		Sort:      sortSpec,
		Group:     group,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

func parseHistoryRecordPayload(payload map[string]any) (RecordDto, *HistoryError) {
	id, herr := payloadStr(payload, "id")
	if herr != nil {
		return RecordDto{}, herr
	}
	rev, ok := parseExactInt64(payload["revision"])
	if !ok {
		return RecordDto{}, histFailed(nil)
	}
	cRaw, herr := payloadStr(payload, "created_at")
	if herr != nil {
		return RecordDto{}, herr
	}
	uRaw, herr := payloadStr(payload, "updated_at")
	if herr != nil {
		return RecordDto{}, herr
	}
	createdAt, herr := normalizeHistoryTimestamp(cRaw)
	if herr != nil {
		return RecordDto{}, herr
	}
	updatedAt, herr := normalizeHistoryTimestamp(uRaw)
	if herr != nil {
		return RecordDto{}, herr
	}
	return RecordDto{
		ID:        id,
		Revision:  rev,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Values:    map[string]any{},
	}, nil
}
