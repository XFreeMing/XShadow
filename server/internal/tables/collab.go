package tables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

const (
	conflictMessage = "这条记录已在其他窗口更新，刚才的修改没有写入"
	changesPageSize = 200
)

// ChangeDto represents a single table change event in GET /changes.
type ChangeDto struct {
	Seq            int64          `json:"seq"`
	EventType      string         `json:"event_type"`
	Topic          string         `json:"topic"`
	EntityID       string         `json:"entity_id"`
	RecordID       string         `json:"record_id"`
	BatchID        *string        `json:"batch_id,omitempty"`
	BaseRevision   int64          `json:"base_revision"`
	ClientRevision int64          `json:"client_revision"`
	RecordRevision int64          `json:"record_revision"`
	Fields         map[string]any `json:"fields"`
	Actor          ActorDto       `json:"actor"`
	CreatedAt      string         `json:"created_at"`
}

// PresenceDto represents an online collaborator cursor on a table.
type PresenceDto struct {
	Actor    ActorDto `json:"actor"`
	RecordID *string  `json:"record_id"`
	FieldID  *string  `json:"field_id"`
	Online   bool     `json:"online"`
}

// CollabSnapshot is returned by GET /changes, GET /presence, and POST /presence.
type CollabSnapshot struct {
	Latest    int64         `json:"latest"`
	NextSince int64         `json:"next_since"`
	Changes   []ChangeDto   `json:"changes"`
	Presence  []PresenceDto `json:"presence"`
}

// Decision represents the optimistic-concurrency merge outcome.
type Decision struct {
	Apply bool
	Base  int64
}

type revisionStep struct {
	base   int64
	result int64
	fields map[string]struct{}
}

// Decide determines whether an edit based on expectedRevision can be applied
// directly or replayed cleanly against disjoint intermediate steps.
func Decide(current, expected int64, steps []revisionStep, patch map[string]struct{}) Decision {
	if expected <= 0 || current <= 0 || expected > current {
		return Decision{Apply: false}
	}
	if expected == current {
		return Decision{Apply: true, Base: current}
	}
	cursor := expected
	touched := make(map[string]struct{})
	for cursor < current {
		var found *revisionStep
		count := 0
		for i := range steps {
			if steps[i].base == cursor {
				found = &steps[i]
				count++
			}
		}
		if count != 1 || found.result != cursor+1 {
			return Decision{Apply: false}
		}
		for f := range found.fields {
			touched[f] = struct{}{}
		}
		cursor = found.result
	}
	if cursor != current {
		return Decision{Apply: false}
	}
	for f := range patch {
		if _, overlap := touched[f]; overlap {
			return Decision{Apply: false}
		}
	}
	return Decision{Apply: true, Base: current}
}

// DigestStr computes the 32-hex-char FNV-1a 64-bit + length digest matching Rust's collab::digest_str.
func DigestStr(text string) string {
	var hash uint64 = 0xcbf29ce484222325
	for i := 0; i < len(text); i++ {
		hash ^= uint64(text[i])
		hash *= 0x100000001b3
	}
	return fmt.Sprintf("%016x%016x", hash, uint64(len(text)))
}

func digestPatch(values map[string]cellPatch) (string, error) {
	// Serialize keys sorted alphabetically with nil for cleared cells,
	// matching Rust's serde_json::to_string(&BTreeMap<String, Option<Value>>).
	plain := make(map[string]any, len(values))
	for k, v := range values {
		if v.valid {
			plain[k] = v.value
		} else {
			plain[k] = nil
		}
	}
	encoded, err := marshalCompactString(plain)
	if err != nil {
		return "", errFailed(err)
	}
	return DigestStr(encoded), nil
}

// ActorColor deterministically assigns one of 6 collaborator badge colors.
func ActorColor(id string) string {
	colors := [6]string{"#7B67EE", "#FF8A00", "#30C28B", "#F25F8A", "#3B82F6", "#E6A100"}
	var hash uint32
	for i := 0; i < len(id); i++ {
		hash = hash*31 + uint32(id[i])
	}
	return colors[int(hash)%len(colors)]
}

func actorDTO(source, id string, taskID *string, label string) ActorDto {
	return ActorDto{
		Source: source,
		ID:     id,
		TaskID: taskID,
		Label:  label,
		Color:  ActorColor(id),
	}
}

func UpdateRecord(
	ctx context.Context,
	pool Pool,
	workspace, tableID, recordID string,
	expectedRevision int64,
	values map[string]any,
	actor Actor,
) (RecordDto, error) {
	if len(values) == 0 || expectedRevision <= 0 {
		return RecordDto{}, errInvalid("没有要保存的字段")
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return RecordDto{}, err
	}
	defer tx.Rollback(ctx)
	touched := mapKeys(values)
	lockedTables, err := prepareTableLocks(ctx, tx, workspace, tableID, touched)
	if err != nil {
		return RecordDto{}, err
	}
	fields, err := loadFields(ctx, tx, workspace, tableID)
	if err != nil {
		return RecordDto{}, err
	}
	saved, err := updateRecordInTx(ctx, tx, workspace, tableID, fields, lockedTables, recordID, expectedRevision, values, actor, nil)
	if err != nil {
		return RecordDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return RecordDto{}, err
	}
	return saved, nil
}

func updateRecordInTx(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID string,
	fields []FieldDto,
	lockedTables []string,
	recordID string,
	expectedRevision int64,
	values map[string]any,
	actor Actor,
	batchID *string,
) (RecordDto, error) {
	if len(values) == 0 || expectedRevision <= 0 {
		return RecordDto{}, errInvalid("没有要保存的字段")
	}
	current, err := lockRecord(ctx, tx, workspace, tableID, recordID)
	if err != nil {
		return RecordDto{}, err
	}
	normalized, err := normalizeValues(fields, values)
	if err != nil {
		return RecordDto{}, err
	}
	d, err := digestPatch(normalized)
	if err != nil {
		return RecordDto{}, err
	}
	fresh, err := loadRecord(ctx, tx, workspace, fields, recordID)
	if err != nil {
		return RecordDto{}, err
	}
	if batchID == nil {
		same, err := sameWrite(ctx, tx, workspace, tableID, recordID, expectedRevision, actor, d)
		if err != nil {
			return RecordDto{}, err
		}
		if same && cellsMatch(fresh, normalized) {
			return fresh, nil
		}
	}
	steps, err := loadRevisionSteps(ctx, tx, workspace, tableID, recordID)
	if err != nil {
		return RecordDto{}, err
	}
	patchSet := make(map[string]struct{}, len(normalized))
	for k := range normalized {
		patchSet[k] = struct{}{}
	}
	decision := Decide(current.Revision, expectedRevision, steps, patchSet)
	if !decision.Apply {
		return RecordDto{}, errConflict(conflictMessage, fresh)
	}
	reversePlan, err := validateAndPlanReverse(ctx, tx, workspace, fields, fresh.Values, normalized, lockedTables)
	if err != nil {
		return RecordDto{}, err
	}
	if err := validateAttachmentRefs(ctx, tx, workspace, tableID, fields, normalized); err != nil {
		return RecordDto{}, err
	}
	if err := writeCells(ctx, tx, workspace, recordID, normalized); err != nil {
		return RecordDto{}, err
	}
	if err := bumpRecordRevision(ctx, tx, workspace, tableID, recordID); err != nil {
		return RecordDto{}, err
	}
	savedRev := current.Revision + 1
	if err := insertChange(ctx, tx, changeWrite{
		workspace:      workspace,
		table:          tableID,
		record:         recordID,
		base:           decision.Base,
		clientRevision: expectedRevision,
		recordRevision: savedRev,
		values:         normalized,
		actor:          actor,
		digest:         d,
		batchID:        batchID,
	}); err != nil {
		return RecordDto{}, err
	}
	if err := applyReversePlan(ctx, tx, workspace, recordID, reversePlan, actor, batchID); err != nil {
		return RecordDto{}, err
	}
	return loadRecord(ctx, tx, workspace, fields, recordID)
}

func cellsMatch(record RecordDto, patch map[string]cellPatch) bool {
	for id, p := range patch {
		cur := record.Values[id]
		var want any
		if p.valid {
			want = p.value
		}
		if !jsonEqual(cur, want) {
			return false
		}
	}
	return true
}

func sameWrite(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID, recordID string,
	clientRevision int64,
	actor Actor,
	digest string,
) (bool, error) {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM generic_change
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid
		   AND event_type = 'record.updated'
		   AND client_revision = $4 AND actor_id = $5::uuid AND op_digest = $6
		 LIMIT 1`,
		workspace, tableID, recordID, clientRevision, actor.ID, digest,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, logFailed(err)
	}
	return true, nil
}

func loadRevisionSteps(ctx context.Context, tx pgx.Tx, workspace, tableID, recordID string) ([]revisionStep, error) {
	rows, err := tx.Query(ctx,
		`SELECT base_revision, record_revision, fields::text
		 FROM generic_change
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid
		   AND event_type = 'record.updated'
		 ORDER BY base_revision`,
		workspace, tableID, recordID,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	var steps []revisionStep
	for rows.Next() {
		var base, result int64
		var raw string
		if err := rows.Scan(&base, &result, &raw); err != nil {
			return nil, logFailed(err)
		}
		var fieldsMap map[string]any
		if err := json.Unmarshal([]byte(raw), &fieldsMap); err != nil {
			return nil, errFailed(err)
		}
		keySet := make(map[string]struct{}, len(fieldsMap))
		for k := range fieldsMap {
			keySet[k] = struct{}{}
		}
		steps = append(steps, revisionStep{base: base, result: result, fields: keySet})
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return steps, nil
}

func bumpRecordRevision(ctx context.Context, tx pgx.Tx, workspace, tableID, recordID string) error {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`UPDATE generic_record
		 SET revision = revision + 1, updated_at = now()
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
		 RETURNING id::text, revision, %s, %s`, created, updated)
	var id, c, u string
	var rev int64
	if err := tx.QueryRow(ctx, sql, recordID, tableID, workspace).Scan(&id, &rev, &c, &u); err != nil {
		return logFailed(err)
	}
	return nil
}

func nextChangeSeq(ctx context.Context, tx pgx.Tx, workspace, tableID string) (int64, error) {
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM generic_change WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		workspace, tableID,
	).Scan(&seq); err != nil {
		return 0, logFailed(err)
	}
	return seq, nil
}

type changeWrite struct {
	workspace      string
	table          string
	record         string
	base           int64
	clientRevision int64
	recordRevision int64
	values         map[string]cellPatch
	actor          Actor
	digest         string
	batchID        *string
}

type eventWrite struct {
	workspace      string
	table          string
	eventType      string
	topic          string
	entityID       string
	recordID       *string
	batchID        *string
	baseRevision   int64
	clientRevision int64
	recordRevision int64
	values         map[string]cellPatch
	actor          Actor
	digest         string
}

func insertChange(ctx context.Context, tx pgx.Tx, w changeWrite) error {
	recCopy := w.record
	return insertEvent(ctx, tx, eventWrite{
		workspace:      w.workspace,
		table:          w.table,
		eventType:      "record.updated",
		topic:          "record",
		entityID:       w.record,
		recordID:       &recCopy,
		batchID:        w.batchID,
		baseRevision:   w.base,
		clientRevision: w.clientRevision,
		recordRevision: w.recordRevision,
		values:         w.values,
		actor:          w.actor,
		digest:         w.digest,
	})
}

func insertEvent(ctx context.Context, tx pgx.Tx, w eventWrite) error {
	seq, err := nextChangeSeq(ctx, tx, w.workspace, w.table)
	if err != nil {
		return err
	}
	fields := make(map[string]any, len(w.values))
	for id, v := range w.values {
		if v.valid {
			fields[id] = v.value
		} else {
			fields[id] = nil
		}
	}
	encoded, err := marshalCompactString(fields)
	if err != nil {
		return errFailed(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO generic_change (
		    workspace_id, table_id, seq, event_type, topic, entity_id, record_id, batch_id,
		    base_revision, client_revision, record_revision,
		    fields, actor_source, actor_id, task_id, actor_label, op_digest
		 ) VALUES (
		    $1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7::uuid, $8::uuid,
		    $9, $10, $11, $12::jsonb, $13, $14::uuid, $15::uuid, $16, $17
		 )`,
		w.workspace, w.table, seq, w.eventType, w.topic, w.entityID, w.recordID, w.batchID,
		w.baseRevision, w.clientRevision, w.recordRevision,
		encoded, w.actor.Source, w.actor.ID, w.actor.TaskID, w.actor.Label, w.digest,
	); err != nil {
		return logFailed(err)
	}
	return nil
}

func ensureTableExists(ctx context.Context, tx pgx.Tx, workspace, tableID string) error {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid`,
		tableID, workspace,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound("找不到这张表")
	}
	if err != nil {
		return logFailed(err)
	}
	return nil
}

func ListChangesSince(ctx context.Context, pool Pool, workspace, tableID string, since int64) (CollabSnapshot, error) {
	if since < 0 {
		return CollabSnapshot{}, errInvalid("变更序号无效")
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return CollabSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return CollabSnapshot{}, logFailed(err)
	}
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return CollabSnapshot{}, err
	}
	latest, err := latestChangeSeq(ctx, tx, workspace, tableID)
	if err != nil {
		return CollabSnapshot{}, err
	}
	changes, err := loadChanges(ctx, tx, workspace, tableID, since)
	if err != nil {
		return CollabSnapshot{}, err
	}
	presence, err := loadPresence(ctx, tx, workspace, tableID)
	if err != nil {
		return CollabSnapshot{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return CollabSnapshot{}, err
	}
	nextSince := since
	if len(changes) > 0 {
		nextSince = changes[len(changes)-1].Seq
	}
	return CollabSnapshot{
		Latest:    latest,
		NextSince: nextSince,
		Changes:   changes,
		Presence:  presence,
	}, nil
}

func latestChangeSeq(ctx context.Context, tx pgx.Tx, workspace, tableID string) (int64, error) {
	var latest int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM generic_change WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		workspace, tableID,
	).Scan(&latest); err != nil {
		return 0, logFailed(err)
	}
	return latest, nil
}

func loadChanges(ctx context.Context, tx pgx.Tx, workspace, tableID string, since int64) ([]ChangeDto, error) {
	created := tsExpr("created_at")
	fetchLimit := changesPageSize + 1
	sql := fmt.Sprintf(
		`SELECT seq, event_type, topic, COALESCE(entity_id, record_id)::text, record_id::text,
		        batch_id::text, base_revision, client_revision, record_revision, fields::text,
		        actor_source, actor_id::text, task_id::text, actor_label, %s
		 FROM generic_change
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND seq > $3
		 ORDER BY seq
		 LIMIT %d`, created, fetchLimit)
	rows, err := tx.Query(ctx, sql, workspace, tableID, since)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	changes := make([]ChangeDto, 0)
	for rows.Next() {
		var c ChangeDto
		var recordID *string
		var rawFields, source, actorID, label string
		var taskID *string
		if err := rows.Scan(
			&c.Seq, &c.EventType, &c.Topic, &c.EntityID, &recordID,
			&c.BatchID, &c.BaseRevision, &c.ClientRevision, &c.RecordRevision, &rawFields,
			&source, &actorID, &taskID, &label, &c.CreatedAt,
		); err != nil {
			return nil, logFailed(err)
		}
		if recordID != nil {
			c.RecordID = *recordID
		}
		var fieldsMap map[string]any
		if err := json.Unmarshal([]byte(rawFields), &fieldsMap); err != nil {
			return nil, errFailed(err)
		}
		if fieldsMap == nil {
			fieldsMap = map[string]any{}
		}
		c.Fields = fieldsMap
		c.Actor = actorDTO(source, actorID, taskID, label)
		changes = append(changes, c)
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return trimSplitBatchPage(changes, changesPageSize), nil
}

func trimSplitBatchPage(changes []ChangeDto, pageLimit int) []ChangeDto {
	if len(changes) <= pageLimit {
		return changes
	}
	nextBatchID := changes[pageLimit].BatchID
	changes = changes[:pageLimit]
	if nextBatchID != nil && len(changes) > 0 {
		last := changes[len(changes)-1]
		if last.BatchID != nil && *last.BatchID == *nextBatchID {
			keepLen := 0
			for i := len(changes) - 1; i >= 0; i-- {
				if changes[i].BatchID == nil || *changes[i].BatchID != *nextBatchID {
					keepLen = i + 1
					break
				}
			}
			if keepLen > 0 {
				changes = changes[:keepLen]
			}
		}
	}
	return changes
}

func GetPresence(ctx context.Context, pool Pool, workspace, tableID string) (CollabSnapshot, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return CollabSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return CollabSnapshot{}, logFailed(err)
	}
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return CollabSnapshot{}, err
	}
	latest, err := latestChangeSeq(ctx, tx, workspace, tableID)
	if err != nil {
		return CollabSnapshot{}, err
	}
	presence, err := loadPresence(ctx, tx, workspace, tableID)
	if err != nil {
		return CollabSnapshot{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return CollabSnapshot{}, err
	}
	return CollabSnapshot{
		Latest:    latest,
		NextSince: latest,
		Changes:   make([]ChangeDto, 0),
		Presence:  presence,
	}, nil
}

func TouchPresence(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	actor Actor,
	recordID, fieldID *string,
) (CollabSnapshot, error) {
	if fieldID != nil && recordID == nil {
		return CollabSnapshot{}, errInvalid("字段位置需要一条记录")
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return CollabSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return CollabSnapshot{}, err
	}
	if recordID != nil {
		var one int
		err := tx.QueryRow(ctx,
			`SELECT 1 FROM generic_record WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid`,
			*recordID, tableID, workspace,
		).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return CollabSnapshot{}, errNotFound("找不到这条记录")
		}
		if err != nil {
			return CollabSnapshot{}, logFailed(err)
		}
	}
	if fieldID != nil {
		var one int
		err := tx.QueryRow(ctx,
			`SELECT 1 FROM generic_field WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid`,
			*fieldID, tableID, workspace,
		).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return CollabSnapshot{}, errNotFound("找不到这个字段")
		}
		if err != nil {
			return CollabSnapshot{}, logFailed(err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO generic_presence (
		    workspace_id, table_id, actor_id, actor_source, task_id, actor_label, record_id, field_id, lease_until
		 ) VALUES (
		    $1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6, $7::uuid, $8::uuid, now() + interval '8 seconds'
		 )
		 ON CONFLICT (workspace_id, table_id, actor_id)
		 DO UPDATE SET actor_source = EXCLUDED.actor_source, task_id = EXCLUDED.task_id,
		               actor_label = EXCLUDED.actor_label, record_id = EXCLUDED.record_id,
		               field_id = EXCLUDED.field_id, lease_until = EXCLUDED.lease_until, updated_at = now()`,
		workspace, tableID, actor.ID, actor.Source, actor.TaskID, actor.Label, recordID, fieldID,
	); err != nil {
		return CollabSnapshot{}, logFailed(err)
	}
	latest, err := latestChangeSeq(ctx, tx, workspace, tableID)
	if err != nil {
		return CollabSnapshot{}, err
	}
	presence, err := loadPresence(ctx, tx, workspace, tableID)
	if err != nil {
		return CollabSnapshot{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return CollabSnapshot{}, err
	}
	return CollabSnapshot{
		Latest:    latest,
		NextSince: latest,
		Changes:   make([]ChangeDto, 0),
		Presence:  presence,
	}, nil
}

func loadPresence(ctx context.Context, tx pgx.Tx, workspace, tableID string) ([]PresenceDto, error) {
	rows, err := tx.Query(ctx,
		`SELECT actor_source, actor_id::text, task_id::text, actor_label, record_id::text, field_id::text
		 FROM generic_presence
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND lease_until > now()
		 ORDER BY actor_label, actor_id`,
		workspace, tableID,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	out := make([]PresenceDto, 0)
	for rows.Next() {
		var source, id, label string
		var taskID, recID, fldID *string
		if err := rows.Scan(&source, &id, &taskID, &label, &recID, &fldID); err != nil {
			return nil, logFailed(err)
		}
		out = append(out, PresenceDto{
			Actor:    actorDTO(source, id, taskID, label),
			RecordID: recID,
			FieldID:  fldID,
			Online:   true,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return out, nil
}

// Keep unused sort import check satisfied if needed.
var _ = sort.Strings
