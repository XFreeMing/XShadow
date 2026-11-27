package tables

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
)

const maxRequestBodyBytes = 1 << 20

// Handler serves all generic-table HTTP routes directly in-process inside the Go server.
// Membership and workspace isolation are enforced by the surrounding router middleware.
type Handler struct {
	Pool       Pool
	AgentLabel func(ctx context.Context, workspaceID, agentID string) string
	router     chi.Router
}

// NewHandler builds a ready-to-serve generic-tables HTTP handler.
func NewHandler(pool Pool, agentLabel func(ctx context.Context, workspaceID, agentID string) string) *Handler {
	h := &Handler{
		Pool:       pool,
		AgentLabel: agentLabel,
	}
	r := chi.NewRouter()
	r.Get("/", h.handleListTables)
	r.Post("/", h.handleCreateTable)
	r.Post("/templates/import", h.handleImportTemplate)
	r.Get("/{id}/template", h.handleExportTemplate)
	r.Get("/{id}", h.handleGetSheet)
	r.Patch("/{id}", h.handleRenameTable)
	r.Delete("/{id}", h.handleDeleteTable)
	r.Get("/{id}/fields", h.handleListFields)
	r.Post("/{id}/fields", h.handleCreateField)
	r.Get("/{id}/fields/{fieldId}", h.handleGetField)
	r.Patch("/{id}/fields/{fieldId}", h.handleUpdateField)
	r.Delete("/{id}/fields/{fieldId}", h.handleDeleteField)
	r.Get("/{id}/record-options", h.handleListRecordOptions)
	r.Get("/{id}/records", h.handleListRecords)
	r.Post("/{id}/records", h.handleCreateRecord)
	r.Post("/{id}/records/batch", h.handleBatchRecords)
	r.Get("/{id}/records/{recordId}", h.handleGetRecord)
	r.Patch("/{id}/records/{recordId}", h.handleUpdateRecord)
	r.Delete("/{id}/records/{recordId}", h.handleDeleteRecord)
	r.Get("/{id}/records/{recordId}/comments", h.handleListComments)
	r.Post("/{id}/records/{recordId}/comments", h.handleCreateComment)
	r.Get("/{id}/changes", h.handleListChanges)
	r.Get("/{id}/history", h.handleListHistory)
	r.Get("/{id}/history/snapshot", h.handleGetHistorySnapshot)
	r.Get("/{id}/history/deleted", h.handleListHistoryDeleted)
	r.Get("/{id}/presence", h.handleGetPresence)
	r.Post("/{id}/presence", h.handleTouchPresence)
	r.Get("/{id}/views", h.handleListViews)
	r.Post("/{id}/views", h.handleCreateView)
	r.Get("/{id}/views/{viewId}", h.handleGetView)
	r.Patch("/{id}/views/{viewId}", h.handleSaveView)
	r.Delete("/{id}/views/{viewId}", h.handleDeleteView)
	r.Post("/{id}/views/{viewId}/submit", h.handleSubmitForm)
	h.router = r
	return h
}

type ctxKey int

const (
	ctxKeyWorkspace ctxKey = iota + 1
	ctxKeyActor
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Pool == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "tables_unavailable", "表格服务未启用")
		return
	}
	workspaceID := strings.TrimSpace(middleware.WorkspaceIDFromContext(r.Context()))
	if !IsUUID(workspaceID) {
		writeErrorJSON(w, http.StatusBadRequest, "invalid_workspace", "工作区标识无效")
		return
	}
	workspaceID = strings.ToLower(workspaceID)

	actor, actorOK := h.resolveTrustedActor(r, workspaceID)
	mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
	if r.Header.Get("X-Actor-Source") == "task_token" && !actorOK {
		writeErrorJSON(w, http.StatusUnauthorized, "unauthorized", "协作者身份无效")
		return
	}
	if !actorOK && mutating {
		writeErrorJSON(w, http.StatusUnauthorized, "unauthorized", "协作者身份无效")
		return
	}

	ctx := context.WithValue(r.Context(), ctxKeyWorkspace, workspaceID)
	if actorOK {
		ctx = context.WithValue(ctx, ctxKeyActor, actor)
	}
	// Reset chi RouteContext so inner routes match independently of outer /api/tables mount.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, chi.NewRouteContext())

	subReq := r.Clone(ctx)
	subReq.URL.Path = normalizeSubPath(r.URL.Path)
	h.router.ServeHTTP(w, subReq)
}

func normalizeSubPath(path string) string {
	rest := strings.TrimPrefix(path, "/api/tables")
	if rest == "" || rest == "/" {
		return "/"
	}
	if !strings.HasPrefix(rest, "/") {
		return "/" + rest
	}
	return rest
}

func (h *Handler) resolveTrustedActor(r *http.Request, workspaceID string) (Actor, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
		taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
		if !IsUUID(agentID) || !IsUUID(taskID) {
			return Actor{}, false
		}
		agentLower := strings.ToLower(agentID)
		taskLower := strings.ToLower(taskID)
		rawLabel := ""
		if h.AgentLabel != nil {
			rawLabel = h.AgentLabel(r.Context(), workspaceID, agentLower)
		}
		return Actor{
			Source: "agent",
			ID:     agentLower,
			TaskID: &taskLower,
			Label:  cleanActorLabel(rawLabel, "agent", agentLower),
		}, true
	}
	member, ok := middleware.MemberFromContext(r.Context())
	if !ok {
		return Actor{}, false
	}
	userID := util.UUIDToString(member.UserID)
	if !IsUUID(userID) {
		return Actor{}, false
	}
	userLower := strings.ToLower(userID)
	return Actor{
		Source: "member",
		ID:     userLower,
		Label:  cleanActorLabel("", "member", userLower),
	}, true
}

func workspaceFromCtx(ctx context.Context) string {
	ws, _ := ctx.Value(ctxKeyWorkspace).(string)
	return ws
}

func optionalActorFromCtx(ctx context.Context) *Actor {
	if a, ok := ctx.Value(ctxKeyActor).(Actor); ok {
		return &a
	}
	return nil
}

func requireActorFromCtx(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	a, ok := r.Context().Value(ctxKeyActor).(Actor)
	if !ok {
		writeInvalid(w, "协作者身份无效")
		return Actor{}, false
	}
	return a, true
}

func readBodyLimited(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		writeInvalid(w, "请求过大或无法读取")
		return nil, false
	}
	return body, true
}

func decodeJSONStrict(w http.ResponseWriter, r *http.Request, dst any, disallowUnknown bool) bool {
	body, ok := readBodyLimited(w, r)
	if !ok {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if disallowUnknown {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		writeInvalid(w, "请求格式不正确")
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeInvalid(w, "请求格式不正确")
		return false
	}
	return true
}

func checkID(w http.ResponseWriter, ids ...string) bool {
	for _, id := range ids {
		if !IsUUID(id) {
			writeInvalid(w, "标识无效")
			return false
		}
	}
	return true
}

func parseOptionalInt64Query(w http.ResponseWriter, r *http.Request, key string) (*int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		writeInvalid(w, "查询参数无效")
		return nil, false
	}
	return &v, true
}

func parseOptionalUUID(raw *string) (*string, bool) {
	if raw == nil || *raw == "" {
		return nil, true
	}
	if !IsUUID(*raw) {
		return nil, false
	}
	v := *raw
	return &v, true
}

// --- Handlers ---

func (h *Handler) handleListTables(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	tables, err := ListTables(r.Context(), h.Pool, ws)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

type createTableBody struct {
	Name   string               `json:"name"`
	Fields []TemplateFieldInput `json:"fields"`
}

func (h *Handler) handleCreateTable(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	var body createTableBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if len(body.Fields) > 0 {
		created, terr := CreateTableWithFields(r.Context(), h.Pool, ws, body.Name, body.Fields)
		if terr != nil {
			writeTemplateErr(w, terr)
			return
		}
		writeOKJSON(w, http.StatusCreated, created)
		return
	}
	created, err := CreateTable(r.Context(), h.Pool, ws, body.Name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusCreated, created)
}

func (h *Handler) handleExportTemplate(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var rawSamples *string
	if r.URL.Query().Has("sample_record_ids") {
		v := r.URL.Query().Get("sample_record_ids")
		rawSamples = &v
	}
	samples, terr := ParseSampleRecordIDsQuery(rawSamples)
	if terr != nil {
		writeTemplateErr(w, terr)
		return
	}
	tmpl, terr := ExportTemplate(r.Context(), h.Pool, ws, id, samples)
	if terr != nil {
		writeTemplateErr(w, terr)
		return
	}
	writeOKJSON(w, http.StatusOK, tmpl)
}

func (h *Handler) handleImportTemplate(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	var body TemplateImportRequest
	if !decodeJSONStrict(w, r, &body, true) {
		return
	}
	actor, ok := requireActorFromCtx(w, r)
	if !ok {
		return
	}
	created, terr := ImportTemplate(r.Context(), h.Pool, ws, body, &actor)
	if terr != nil {
		writeTemplateErr(w, terr)
		return
	}
	writeOKJSON(w, http.StatusCreated, created)
}

func (h *Handler) handleGetSheet(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var viewID *string
	if raw := r.URL.Query().Get("view"); raw != "" {
		if !checkID(w, raw) {
			return
		}
		viewID = &raw
	}
	sheet, err := GetSheet(r.Context(), h.Pool, ws, id, viewID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, sheet)
}

type nameBody struct {
	Name string `json:"name"`
}

func (h *Handler) handleRenameTable(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var body nameBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	table, err := RenameTable(r.Context(), h.Pool, ws, id, body.Name, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"table": table})
}

func (h *Handler) handleDeleteTable(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	deleted, err := DeleteTable(r.Context(), h.Pool, ws, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, deleted)
}

func (h *Handler) handleListFields(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	fields, err := ListFields(r.Context(), h.Pool, ws, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"fields": fields})
}

func (h *Handler) handleGetField(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	fieldID := chi.URLParam(r, "fieldId")
	if !checkID(w, id, fieldID) {
		return
	}
	field, err := GetField(r.Context(), h.Pool, ws, id, fieldID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"field": field})
}

type createFieldBody struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Config any    `json:"config"`
}

func (h *Handler) handleCreateField(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var body createFieldBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	field, err := CreateField(r.Context(), h.Pool, ws, id, body.Name, body.Kind, body.Config, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusCreated, map[string]any{"field": field})
}

type optionalJSONField struct {
	set   bool
	value any
}

func (o *optionalJSONField) UnmarshalJSON(data []byte) error {
	o.set = true
	return json.Unmarshal(data, &o.value)
}

type fieldPatchBody struct {
	Name   *string           `json:"name"`
	Config optionalJSONField `json:"config"`
}

func (h *Handler) handleUpdateField(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	fieldID := chi.URLParam(r, "fieldId")
	if !checkID(w, id, fieldID) {
		return
	}
	var body fieldPatchBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	hasCfg := body.Config.set && body.Config.value != nil
	field, err := UpdateField(r.Context(), h.Pool, ws, id, fieldID, body.Name, hasCfg, body.Config.value, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"field": field})
}

func (h *Handler) handleDeleteField(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	fieldID := chi.URLParam(r, "fieldId")
	if !checkID(w, id, fieldID) {
		return
	}
	deleted, err := DeleteField(r.Context(), h.Pool, ws, id, fieldID, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, deleted)
}

func (h *Handler) handleListRecordOptions(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	records, err := ListRecordOptions(r.Context(), h.Pool, ws, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"records": records})
}

func (h *Handler) handleListRecords(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var viewID *string
	if raw := r.URL.Query().Get("view"); raw != "" {
		if !checkID(w, raw) {
			return
		}
		viewID = &raw
	}
	limit, ok := parseOptionalInt64Query(w, r, "limit")
	if !ok {
		return
	}
	offset, ok := parseOptionalInt64Query(w, r, "offset")
	if !ok {
		return
	}
	list, err := ListRecords(r.Context(), h.Pool, ws, id, viewID, limit, offset)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetRecord(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	recordID := chi.URLParam(r, "recordId")
	if !checkID(w, id, recordID) {
		return
	}
	record, err := GetRecord(r.Context(), h.Pool, ws, id, recordID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"record": record})
}

type recordBody struct {
	Values map[string]any `json:"values"`
}

func (h *Handler) handleCreateRecord(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var body recordBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if body.Values == nil {
		body.Values = map[string]any{}
	}
	for fieldID := range body.Values {
		if !IsUUID(fieldID) {
			writeInvalid(w, "字段标识无效")
			return
		}
	}
	record, err := CreateRecord(r.Context(), h.Pool, ws, id, body.Values, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusCreated, map[string]any{"record": record})
}

type recordPatchBody struct {
	ExpectedRevision *int64         `json:"expected_revision"`
	Values           map[string]any `json:"values"`
}

func (h *Handler) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	recordID := chi.URLParam(r, "recordId")
	if !checkID(w, id, recordID) {
		return
	}
	var body recordPatchBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if body.ExpectedRevision == nil || body.Values == nil {
		writeInvalid(w, "请求格式不正确")
		return
	}
	for fieldID := range body.Values {
		if !IsUUID(fieldID) {
			writeInvalid(w, "字段标识无效")
			return
		}
	}
	actor, ok := requireActorFromCtx(w, r)
	if !ok {
		return
	}
	record, err := UpdateRecord(r.Context(), h.Pool, ws, id, recordID, *body.ExpectedRevision, body.Values, actor)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"record": record})
}

type deleteRecordBody struct {
	ExpectedRevision *int64 `json:"expected_revision"`
}

func (h *Handler) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	recordID := chi.URLParam(r, "recordId")
	if !checkID(w, id, recordID) {
		return
	}
	queryRev, ok := parseOptionalInt64Query(w, r, "expected_revision")
	if !ok {
		return
	}
	rawBody, ok := readBodyLimited(w, r)
	if !ok {
		return
	}
	var bodyRev *int64
	if len(bytes.TrimSpace(rawBody)) > 0 {
		var parsed deleteRecordBody
		if err := json.Unmarshal(rawBody, &parsed); err != nil || parsed.ExpectedRevision == nil {
			writeInvalid(w, "请求格式不正确")
			return
		}
		bodyRev = parsed.ExpectedRevision
	}
	var expectedRev int64
	switch {
	case queryRev != nil && bodyRev != nil:
		if *queryRev != *bodyRev {
			writeInvalid(w, "记录版本号不一致")
			return
		}
		expectedRev = *queryRev
	case queryRev != nil:
		expectedRev = *queryRev
	case bodyRev != nil:
		expectedRev = *bodyRev
	default:
		writeInvalid(w, "删除记录需要提供 expected_revision")
		return
	}
	if expectedRev <= 0 {
		writeInvalid(w, "记录版本号无效")
		return
	}
	actor, ok := requireActorFromCtx(w, r)
	if !ok {
		return
	}
	deleted, err := DeleteRecord(r.Context(), h.Pool, ws, id, recordID, expectedRev, actor)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, deleted)
}

func (h *Handler) handleBatchRecords(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var raw any
	if !decodeJSONStrict(w, r, &raw, false) {
		return
	}
	actor, ok := requireActorFromCtx(w, r)
	if !ok {
		return
	}
	req, berr := ParseBatchRequest(raw)
	if berr != nil {
		writeBatchErr(w, berr)
		return
	}
	result, berr := ExecuteBatch(r.Context(), h.Pool, ws, id, actor, req)
	if berr != nil {
		writeBatchErr(w, berr)
		return
	}
	writeOKJSON(w, http.StatusOK, result)
}

func (h *Handler) handleListChanges(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	sinceOpt, ok := parseOptionalInt64Query(w, r, "since")
	if !ok {
		return
	}
	since := int64(0)
	if sinceOpt != nil {
		since = *sinceOpt
	}
	snap, err := ListChangesSince(r.Context(), h.Pool, ws, id, since)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, snap)
}

func (h *Handler) handleGetPresence(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	snap, err := GetPresence(r.Context(), h.Pool, ws, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, snap)
}

type presenceBody struct {
	RecordID *string `json:"record_id"`
	FieldID  *string `json:"field_id"`
}

func (h *Handler) handleTouchPresence(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var body presenceBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	actor, ok := requireActorFromCtx(w, r)
	if !ok {
		return
	}
	recID, ok := parseOptionalUUID(body.RecordID)
	if !ok {
		writeInvalid(w, "标识无效")
		return
	}
	fieldID, ok := parseOptionalUUID(body.FieldID)
	if !ok {
		writeInvalid(w, "标识无效")
		return
	}
	snap, err := TouchPresence(r.Context(), h.Pool, ws, id, actor, recID, fieldID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, snap)
}

func (h *Handler) handleListComments(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	recordID := chi.URLParam(r, "recordId")
	if !checkID(w, id, recordID) {
		return
	}
	after, ok := parseOptionalInt64Query(w, r, "after")
	if !ok {
		return
	}
	limit, ok := parseOptionalInt64Query(w, r, "limit")
	if !ok {
		return
	}
	list, err := ListComments(r.Context(), h.Pool, ws, id, recordID, after, limit)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, list)
}

type commentBody struct {
	Body     *string `json:"body"`
	ParentID *string `json:"parent_id"`
}

func (h *Handler) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	recordID := chi.URLParam(r, "recordId")
	if !checkID(w, id, recordID) {
		return
	}
	var body commentBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if body.Body == nil {
		writeInvalid(w, "请求格式不正确")
		return
	}
	actor, ok := requireActorFromCtx(w, r)
	if !ok {
		return
	}
	parentID, ok := parseOptionalUUID(body.ParentID)
	if !ok {
		writeInvalid(w, "父评论标识无效")
		return
	}
	comment, err := CreateComment(r.Context(), h.Pool, ws, id, recordID, actor, *body.Body, parentID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusCreated, map[string]any{"comment": comment})
}

func (h *Handler) handleListHistory(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	limit, ok := parseOptionalInt64Query(w, r, "limit")
	if !ok {
		return
	}
	var cursor *string
	if r.URL.Query().Has("cursor") {
		c := r.URL.Query().Get("cursor")
		cursor = &c
	}
	list, herr := ListHistory(r.Context(), h.Pool, ws, id, limit, cursor)
	if herr != nil {
		writeHistoryErr(w, herr)
		return
	}
	writeOKJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetHistorySnapshot(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	rev, ok := parseOptionalInt64Query(w, r, "revision")
	if !ok {
		return
	}
	var ts *string
	if r.URL.Query().Has("timestamp") {
		v := r.URL.Query().Get("timestamp")
		ts = &v
	}
	target, herr := ParseSnapshotTarget(rev, ts)
	if herr != nil {
		writeHistoryErr(w, herr)
		return
	}
	snap, herr := GetHistorySnapshot(r.Context(), h.Pool, ws, id, target)
	if herr != nil {
		writeHistoryErr(w, herr)
		return
	}
	writeOKJSON(w, http.StatusOK, snap)
}

func (h *Handler) handleListHistoryDeleted(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	limit, ok := parseOptionalInt64Query(w, r, "limit")
	if !ok {
		return
	}
	var cursor *string
	if r.URL.Query().Has("cursor") {
		c := r.URL.Query().Get("cursor")
		cursor = &c
	}
	list, herr := ListDeletedRecordsHistory(r.Context(), h.Pool, ws, id, limit, cursor)
	if herr != nil {
		writeHistoryErr(w, herr)
		return
	}
	writeOKJSON(w, http.StatusOK, list)
}

type optionalStringField struct {
	set   bool
	value *string
}

func (o *optionalStringField) UnmarshalJSON(data []byte) error {
	o.set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	o.value = &s
	return nil
}

type viewBody struct {
	Name    *string             `json:"name"`
	Kind    *string             `json:"kind"`
	Config  optionalJSONField   `json:"config"`
	Columns []string            `json:"columns"`
	Filter  *StoredFilter       `json:"filter"`
	Sort    *StoredSort         `json:"sort"`
	Group   optionalStringField `json:"group"`
}

func (b viewBody) toViewWrite() ViewWrite {
	name := ""
	if b.Name != nil {
		name = *b.Name
	}
	return ViewWrite{
		Name:    name,
		Kind:    b.Kind,
		Config:  ConfigUpdate{Set: b.Config.set, Value: b.Config.value},
		Columns: b.Columns,
		Filter:  b.Filter,
		Sort:    b.Sort,
		Group:   GroupUpdate{Set: b.Group.set, Value: b.Group.value},
	}
}

func validateViewBodyIDs(w http.ResponseWriter, body viewBody) bool {
	for _, col := range body.Columns {
		if !IsUUID(col) {
			writeInvalid(w, "可见列里的字段标识无效")
			return false
		}
	}
	if body.Filter != nil && !IsUUID(body.Filter.FieldID) {
		writeInvalid(w, "筛选字段标识无效")
		return false
	}
	if body.Sort != nil && !IsUUID(body.Sort.FieldID) {
		writeInvalid(w, "排序字段标识无效")
		return false
	}
	if body.Group.set && body.Group.value != nil && !IsUUID(*body.Group.value) {
		writeInvalid(w, "分组字段标识无效")
		return false
	}
	return true
}

func (h *Handler) handleSubmitForm(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	viewID := chi.URLParam(r, "viewId")
	if !checkID(w, id, viewID) {
		return
	}
	var body recordBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if body.Values == nil {
		body.Values = map[string]any{}
	}
	for fieldID := range body.Values {
		if !IsUUID(fieldID) {
			writeInvalid(w, "字段标识无效")
			return
		}
	}
	record, err := SubmitForm(r.Context(), h.Pool, ws, id, viewID, body.Values, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusCreated, map[string]any{"record": record})
}

func (h *Handler) handleListViews(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	views, err := ListViews(r.Context(), h.Pool, ws, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"views": views})
}

func (h *Handler) handleGetView(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	viewID := chi.URLParam(r, "viewId")
	if !checkID(w, id, viewID) {
		return
	}
	view, err := GetView(r.Context(), h.Pool, ws, id, viewID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"view": view})
}

func (h *Handler) handleCreateView(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if !checkID(w, id) {
		return
	}
	var body viewBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if body.Name == nil {
		writeInvalid(w, "请求格式不正确")
		return
	}
	if !validateViewBodyIDs(w, body) {
		return
	}
	view, err := CreateView(r.Context(), h.Pool, ws, id, body.toViewWrite(), optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusCreated, map[string]any{"view": view})
}

func (h *Handler) handleSaveView(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	viewID := chi.URLParam(r, "viewId")
	if !checkID(w, id, viewID) {
		return
	}
	var body viewBody
	if !decodeJSONStrict(w, r, &body, false) {
		return
	}
	if body.Name == nil {
		writeInvalid(w, "请求格式不正确")
		return
	}
	if !validateViewBodyIDs(w, body) {
		return
	}
	view, err := SaveView(r.Context(), h.Pool, ws, id, viewID, body.toViewWrite(), optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, map[string]any{"view": view})
}

func (h *Handler) handleDeleteView(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	viewID := chi.URLParam(r, "viewId")
	if !checkID(w, id, viewID) {
		return
	}
	deleted, err := DeleteView(r.Context(), h.Pool, ws, id, viewID, optionalActorFromCtx(r.Context()))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeOKJSON(w, http.StatusOK, deleted)
}

// --- Response helpers ---

func writeOKJSON(w http.ResponseWriter, status int, payload any) {
	b, err := marshalCompact(payload)
	if err != nil {
		writeErrorJSON(w, http.StatusInternalServerError, "tables_failed", "表格暂时无法保存")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeErrorJSON(w http.ResponseWriter, status int, code, message string) {
	writeOKJSON(w, status, map[string]any{
		"error": message,
		"code":  code,
	})
}

func writeInvalid(w http.ResponseWriter, message string) {
	writeErrorJSON(w, http.StatusBadRequest, "invalid_value", message)
}

func storeErrParts(err error) (int, map[string]any) {
	se := errFailed(err)
	switch se.Kind {
	case ErrKindNotFound:
		return http.StatusNotFound, map[string]any{"error": se.Message, "code": "not_found"}
	case ErrKindInvalid:
		return http.StatusBadRequest, map[string]any{"error": se.Message, "code": "invalid_value"}
	case ErrKindNameTaken:
		return http.StatusConflict, map[string]any{"error": "这个表里已经有同名字段", "code": "field_name_taken"}
	case ErrKindConflict:
		body := map[string]any{"error": se.Message, "code": "revision_conflict"}
		if se.Record != nil {
			body["record"] = *se.Record
		}
		return http.StatusConflict, body
	case ErrKindReferenceConflict:
		return http.StatusConflict, map[string]any{
			"error": se.Message,
			"code":  se.Code,
			"hint":  se.Hint,
		}
	default:
		return http.StatusInternalServerError, map[string]any{"error": "表格暂时无法保存", "code": "tables_failed"}
	}
}

func writeStoreErr(w http.ResponseWriter, err error) {
	status, body := storeErrParts(err)
	writeOKJSON(w, status, body)
}

func writeBatchErr(w http.ResponseWriter, err *BatchError) {
	var status int
	var body map[string]any
	if err.IdempotencyConflict != "" {
		status = http.StatusConflict
		body = map[string]any{
			"error": err.IdempotencyConflict,
			"code":  "idempotency_conflict",
		}
	} else {
		status, body = storeErrParts(err.StoreErr)
	}
	if err.Index != nil {
		body["failed_index"] = *err.Index
	}
	if err.RecordID != nil {
		body["record_id"] = *err.RecordID
	}
	writeOKJSON(w, status, body)
}

func writeTemplateErr(w http.ResponseWriter, err *TemplateError) {
	switch err.Kind {
	case TemplateErrStore:
		writeStoreErr(w, err.StoreErr)
	case TemplateErrNotFound:
		writeStoreErr(w, errNotFound(err.Message))
	case TemplateErrInvalid:
		writeOKJSON(w, http.StatusBadRequest, map[string]any{
			"error": err.Message,
			"code":  err.Code,
		})
	case TemplateErrUnsupportedDependency:
		writeOKJSON(w, http.StatusConflict, map[string]any{
			"error":        err.Message,
			"code":         "unsupported_template_dependency",
			"dependencies": err.Dependencies,
		})
	default:
		writeStoreErr(w, errFailed(err))
	}
}

func writeHistoryErr(w http.ResponseWriter, err *HistoryError) {
	switch err.Kind {
	case HistoryErrNotFound:
		writeErrorJSON(w, http.StatusNotFound, "not_found", err.Message)
	case HistoryErrNotAvailable:
		writeErrorJSON(w, http.StatusNotFound, "history_not_available", err.Message)
	case HistoryErrInvalid:
		writeInvalid(w, err.Message)
	default:
		writeErrorJSON(w, http.StatusInternalServerError, "tables_failed", "无法读取表格历史")
	}
}
