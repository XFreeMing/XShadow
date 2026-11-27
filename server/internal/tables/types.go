package tables

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool abstracts the pgx pool operations needed by the tables package.
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var _ Pool = (*pgxpool.Pool)(nil)

// StoreErrorKind categorizes generic-table storage and validation errors.
type StoreErrorKind int

const (
	ErrKindFailed StoreErrorKind = iota
	ErrKindNotFound
	ErrKindInvalid
	ErrKindNameTaken
	ErrKindConflict
	ErrKindReferenceConflict
)

// StoreError carries structured HTTP mapping details matching the public API contract.
type StoreError struct {
	Kind    StoreErrorKind
	Code    string
	Message string
	Hint    string
	Record  *RecordDto
}

func (e *StoreError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return "表格暂时无法保存"
}

func errNotFound(msg string) *StoreError {
	return &StoreError{Kind: ErrKindNotFound, Code: "not_found", Message: msg}
}

func errInvalid(msg string) *StoreError {
	return &StoreError{Kind: ErrKindInvalid, Code: "invalid_value", Message: msg}
}

func errNameTaken() *StoreError {
	return &StoreError{Kind: ErrKindNameTaken, Code: "field_name_taken", Message: "这个表里已经有同名字段"}
}

func errConflict(msg string, record RecordDto) *StoreError {
	r := record
	return &StoreError{Kind: ErrKindConflict, Code: "revision_conflict", Message: msg, Record: &r}
}

func errReferenceConflict(code, msg, hint string) *StoreError {
	return &StoreError{Kind: ErrKindReferenceConflict, Code: code, Message: msg, Hint: hint}
}

func errFailed(err error) *StoreError {
	if err != nil {
		var se *StoreError
		if errors.As(err, &se) {
			return se
		}
	}
	return &StoreError{Kind: ErrKindFailed, Code: "tables_failed", Message: "表格暂时无法保存"}
}

func mapWriteErr(err error, retryMsg string) *StoreError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		if strings.Contains(pgErr.ConstraintName, "generic_field_name") ||
			strings.Contains(pgErr.Error(), "generic_field_name") {
			return errNameTaken()
		}
		return errInvalid(retryMsg)
	}
	return errFailed(err)
}

// TableDto represents a workspace generic table.
type TableDto struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// FieldDto represents a field definition in a generic table.
type FieldDto struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Config    any    `json:"config"`
	Position  int32  `json:"position"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// RecordDto represents a single record with its cell values and optional computed errors.
type RecordDto struct {
	ID        string            `json:"id"`
	Revision  int64             `json:"revision"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
	Values    map[string]any    `json:"values"`
	Errors    map[string]string `json:"errors,omitempty"`
}

// StoredFilter is a normalized view filter.
type StoredFilter struct {
	FieldID string `json:"field_id"`
	Op      string `json:"op"`
	Value   any    `json:"value,omitempty"`
}

// StoredSort is a normalized view sort.
type StoredSort struct {
	FieldID   string `json:"field_id"`
	Direction string `json:"direction"`
}

// StoredView is a normalized view ready for database persistence.
type StoredView struct {
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Config  any           `json:"config"`
	Columns []string      `json:"columns"`
	Filter  *StoredFilter `json:"filter"`
	Sort    *StoredSort   `json:"sort"`
	Group   *string       `json:"group"`
}

// ViewDto represents a saved generic-table view.
type ViewDto struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"`
	Config    any           `json:"config"`
	Columns   []string      `json:"columns"`
	Filter    *StoredFilter `json:"filter"`
	Sort      *StoredSort   `json:"sort"`
	Group     *string       `json:"group"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

// SheetDto is the full table snapshot returned by GET /api/tables/{id}.
type SheetDto struct {
	Table        TableDto    `json:"table"`
	Fields       []FieldDto  `json:"fields"`
	Views        []ViewDto   `json:"views"`
	Records      []RecordDto `json:"records"`
	ActiveViewID *string     `json:"active_view_id"`
}

// RecordOptionDto is a compact record choice for relation pickers.
type RecordOptionDto struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// TableCreated is returned when creating a table without inline fields.
type TableCreated struct {
	Table TableDto `json:"table"`
	View  ViewDto  `json:"view"`
}

// DeletedDto is returned by delete endpoints.
type DeletedDto struct {
	Deleted  bool   `json:"deleted"`
	ID       string `json:"id"`
	Revision *int64 `json:"revision,omitempty"`
}

// RecordListDto is the paginated record list response.
type RecordListDto struct {
	Records      []RecordDto `json:"records"`
	Total        int64       `json:"total"`
	Limit        int64       `json:"limit"`
	Offset       int64       `json:"offset"`
	HasMore      bool        `json:"has_more"`
	NextOffset   *int64      `json:"next_offset"`
	ActiveViewID *string     `json:"active_view_id"`
}

// Actor identifies a trusted collaborator (member or task-token agent).
type Actor struct {
	Source string
	ID     string
	TaskID *string
	Label  string
}

// ActorDto is the serialized collaborator identity in changes, comments, and presence.
type ActorDto struct {
	Source string  `json:"source"`
	ID     string  `json:"id"`
	TaskID *string `json:"task_id"`
	Label  string  `json:"label"`
	Color  string  `json:"color"`
}

// marshalCompact encodes v as compact JSON without HTML-escaping `<`, `>`, `&`,
// matching serde_json::to_string output byte-for-byte.
func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func marshalCompactString(v any) (string, error) {
	b, err := marshalCompact(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cloneJSON deep-clones a JSON-compatible value tree (maps, slices, primitives).
func cloneJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = cloneJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = cloneJSON(val)
		}
		return out
	default:
		return x
	}
}

func tsExpr(col string) string {
	return fmt.Sprintf(`to_char(%s AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`, col)
}

func historyTSExpr(col string) string {
	return fmt.Sprintf(`to_char(%s AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`, col)
}

func jsonEqual(a, b any) bool {
	if fa, ok := asFloat64(a); ok {
		if fb, ok := asFloat64(b); ok {
			return fa == fb
		}
		return false
	}
	ba, errA := marshalCompact(a)
	bb, errB := marshalCompact(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ba, bb)
}

func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
