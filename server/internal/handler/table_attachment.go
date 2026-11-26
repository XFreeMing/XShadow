package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TableAttachmentResponse is the client-facing metadata for a generic table
// attachment. It intentionally omits raw storage URLs and object keys; bytes
// are always fetched through the authenticated table-scoped content path.
type TableAttachmentResponse struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	TableID       string `json:"table_id"`
	UploaderType  string `json:"uploader_type"`
	UploaderID    string `json:"uploader_id"`
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	SizeBytes     int64  `json:"size_bytes"`
	ContentSHA256 string `json:"content_sha256"`
	DownloadPath  string `json:"download_path"`
	CreatedAt     string `json:"created_at"`
}

type tableAttachmentRecord struct {
	ID            pgtype.UUID
	WorkspaceID   pgtype.UUID
	TableID       pgtype.UUID
	UploaderType  string
	UploaderID    pgtype.UUID
	Filename      string
	ContentType   string
	SizeBytes     int64
	ContentSHA256 string
	StorageKey    string
	ObjectURL     string
	Status        string
	CreatedAt     pgtype.Timestamptz
}

func tableAttachmentDownloadPath(tableID, attachmentID string) string {
	return "/api/tables/" + tableID + "/attachments/" + attachmentID + "/content"
}

func tableAttachmentToResponse(rec tableAttachmentRecord) TableAttachmentResponse {
	id := uuidToString(rec.ID)
	tableID := uuidToString(rec.TableID)
	return TableAttachmentResponse{
		ID:            id,
		WorkspaceID:   uuidToString(rec.WorkspaceID),
		TableID:       tableID,
		UploaderType:  rec.UploaderType,
		UploaderID:    uuidToString(rec.UploaderID),
		Filename:      rec.Filename,
		ContentType:   rec.ContentType,
		SizeBytes:     rec.SizeBytes,
		ContentSHA256: rec.ContentSHA256,
		DownloadPath:  tableAttachmentDownloadPath(tableID, id),
		CreatedAt:     rec.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
}

func sanitizeTableAttachmentFilename(raw string) string {
	cleaned := strings.ReplaceAll(raw, "\\", "/")
	cleaned = path.Base(cleaned)
	cleaned = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, cleaned)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return "attachment"
	}
	return cleaned
}

func safeTableAttachmentExt(filename string) string {
	ext := path.Ext(filename)
	if len(ext) <= 1 || ext[0] != '.' {
		return ""
	}
	for _, c := range ext[1:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return ext
}

func scanTableAttachment(row pgx.Row) (tableAttachmentRecord, error) {
	var rec tableAttachmentRecord
	err := row.Scan(
		&rec.ID,
		&rec.WorkspaceID,
		&rec.TableID,
		&rec.UploaderType,
		&rec.UploaderID,
		&rec.Filename,
		&rec.ContentType,
		&rec.SizeBytes,
		&rec.ContentSHA256,
		&rec.StorageKey,
		&rec.ObjectURL,
		&rec.Status,
		&rec.CreatedAt,
	)
	return rec, err
}

func (h *Handler) requireTableAttachmentScope(w http.ResponseWriter, r *http.Request) (userID, workspaceID string, wsUUID, tableUUID pgtype.UUID, ok bool) {
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	workspaceID = h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace context required")
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	wsUUID, ok = parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, err := h.getWorkspaceMember(r.Context(), userID, workspaceID); err != nil {
		writeError(w, http.StatusForbidden, "not a member of this workspace")
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	tableUUID, ok = parseUUIDOrBadRequest(w, chi.URLParam(r, "tableId"), "table_id")
	if !ok {
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	var exists int
	err := h.DB.QueryRow(
		r.Context(),
		`SELECT 1 FROM generic_table WHERE id = $1 AND workspace_id = $2`,
		tableUUID, wsUUID,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "table not found")
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	if err != nil {
		slog.Error("failed to verify generic table scope", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to verify table")
		return "", "", pgtype.UUID{}, pgtype.UUID{}, false
	}
	return userID, workspaceID, wsUUID, tableUUID, true
}

// UploadTableAttachment — POST /api/tables/{tableId}/attachments
func (h *Handler) UploadTableAttachment(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeFeatureDisabled(w, "file_upload_not_configured", "file upload not configured")
		return
	}
	if h.TxStarter == nil {
		writeError(w, http.StatusInternalServerError, "database transactions unavailable")
		return
	}
	userID, workspaceID, wsUUID, tableUUID, ok := h.requireTableAttachmentScope(w, r)
	if !ok {
		return
	}

	rawKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if rawKey == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}
	idemUUID, ok := parseUUIDOrBadRequest(w, rawKey, "Idempotency-Key")
	if !ok {
		return
	}

	uploaderType, uploaderID := h.resolveActor(r, userID, workspaceID)
	uploaderUUID, ok := parseUUIDOrBadRequest(w, uploaderID, "uploader_id")
	if !ok {
		return
	}

	err := parseUploadForm(w, r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "file exceeds the 100 MiB upload limit")
		} else {
			writeError(w, http.StatusBadRequest, "invalid multipart form")
		}
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing file field: %v", err))
		return
	}
	defer file.Close()

	filename := sanitizeTableAttachmentFilename(header.Filename)

	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}
	contentType := http.DetectContentType(buf[:n])
	if ct, ok := extContentTypes[strings.ToLower(path.Ext(filename))]; ok {
		contentType = ct
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}

	sum := sha256.Sum256(data)
	contentSHA256 := hex.EncodeToString(sum[:])
	sizeBytes := int64(len(data))

	reserved, replayed, statusCode, errMsg, errCode := h.reserveTableAttachmentIntent(
		r.Context(),
		wsUUID,
		workspaceID,
		tableUUID,
		uploaderType,
		uploaderUUID,
		idemUUID,
		filename,
		contentType,
		sizeBytes,
		contentSHA256,
	)
	if statusCode != 0 {
		if errCode != "" {
			writeJSON(w, statusCode, map[string]string{"error": errMsg, "code": errCode})
			return
		}
		writeError(w, statusCode, errMsg)
		return
	}
	if replayed {
		writeJSON(w, http.StatusOK, map[string]any{
			"attachment": tableAttachmentToResponse(reserved),
		})
		return
	}

	finalized, replayed, statusCode, errMsg, errCode := h.completeTableAttachmentUpload(
		r,
		wsUUID,
		workspaceID,
		tableUUID,
		reserved.ID,
		sizeBytes,
		contentSHA256,
		data,
	)
	if statusCode != 0 {
		if writeRequestCancellation(w, r, r.Context().Err()) {
			return
		}
		if errCode != "" {
			writeJSON(w, statusCode, map[string]string{"error": errMsg, "code": errCode})
			return
		}
		writeError(w, statusCode, errMsg)
		return
	}
	if replayed {
		writeJSON(w, http.StatusOK, map[string]any{
			"attachment": tableAttachmentToResponse(finalized),
		})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"attachment": tableAttachmentToResponse(finalized),
	})
}

func (h *Handler) reserveTableAttachmentIntent(
	ctx context.Context,
	wsUUID pgtype.UUID,
	workspaceID string,
	tableUUID pgtype.UUID,
	uploaderType string,
	uploaderUUID pgtype.UUID,
	idemUUID pgtype.UUID,
	filename string,
	contentType string,
	sizeBytes int64,
	contentSHA256 string,
) (rec tableAttachmentRecord, replayed bool, statusCode int, errMsg string, errCode string) {
	newID, err := uuid.NewV7()
	if err != nil {
		slog.Error("failed to generate table attachment uuid", "error", err)
		return rec, false, http.StatusInternalServerError, "internal error", ""
	}
	storageKey := "workspaces/" + workspaceID + "/" + newID.String() + safeTableAttachmentExt(filename)
	objectURL := h.Storage.ObjectURL(storageKey)
	if !storage.ScopedWorkspaceAttachmentKeyValid(workspaceID, newID.String(), storageKey) {
		return rec, false, http.StatusInternalServerError, "internal error", ""
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}
	defer tx.Rollback(ctx)

	var exists int
	if err := tx.QueryRow(
		ctx,
		`SELECT 1 FROM generic_table WHERE id = $1 AND workspace_id = $2 FOR SHARE`,
		tableUUID, wsUUID,
	).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return rec, false, http.StatusNotFound, "table not found", ""
	} else if err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}

	inserted, err := scanTableAttachment(tx.QueryRow(
		ctx,
		`INSERT INTO generic_table_attachment (
			id, workspace_id, table_id, uploader_type, uploader_id, idempotency_key,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, 'pending'
		)
		ON CONFLICT (workspace_id, table_id, uploader_type, uploader_id, idempotency_key) DO NOTHING
		RETURNING id, workspace_id, table_id, uploader_type, uploader_id,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status, created_at`,
		pgtype.UUID{Bytes: newID, Valid: true},
		wsUUID,
		tableUUID,
		uploaderType,
		uploaderUUID,
		idemUUID,
		filename,
		contentType,
		sizeBytes,
		contentSHA256,
		storageKey,
		objectURL,
	))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return rec, false, http.StatusInternalServerError, "database error", ""
		}
		return inserted, false, 0, "", ""
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}

	existing, err := scanTableAttachment(tx.QueryRow(
		ctx,
		`SELECT id, workspace_id, table_id, uploader_type, uploader_id,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status, created_at
		 FROM generic_table_attachment
		 WHERE workspace_id = $1
		   AND table_id = $2
		   AND uploader_type = $3
		   AND uploader_id = $4
		   AND idempotency_key = $5`,
		wsUUID, tableUUID, uploaderType, uploaderUUID, idemUUID,
	))
	if err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}
	if err := tx.Commit(ctx); err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}

	if existing.ContentSHA256 != contentSHA256 || existing.SizeBytes != sizeBytes {
		return rec, false, http.StatusConflict, "idempotency key already used with different file content", "idempotency_conflict"
	}
	if existing.Status == "ready" {
		return existing, true, 0, "", ""
	}
	return existing, false, 0, "", ""
}

// completeTableAttachmentUpload finishes a reserved upload under a single
// transaction holding generic_table FOR SHARE and the attachment row FOR UPDATE.
// Concurrent same-key uploads serialize on the row lock (second sees ready and
// skips storage I/O); concurrent table deletion waits on the table row lock and
// enqueues cleanup atomically. On storage or commit error/ambiguity, the pending
// intent row is preserved so the same key replays the same object key.
func (h *Handler) completeTableAttachmentUpload(
	r *http.Request,
	wsUUID pgtype.UUID,
	workspaceID string,
	tableUUID pgtype.UUID,
	attachmentID pgtype.UUID,
	sizeBytes int64,
	contentSHA256 string,
	data []byte,
) (rec tableAttachmentRecord, replayed bool, statusCode int, errMsg string, errCode string) {
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}
	defer tx.Rollback(ctx)

	var exists int
	if err := tx.QueryRow(
		ctx,
		`SELECT 1 FROM generic_table WHERE id = $1 AND workspace_id = $2 FOR SHARE`,
		tableUUID, wsUUID,
	).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return rec, false, http.StatusNotFound, "table not found", ""
	} else if err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}

	locked, err := scanTableAttachment(tx.QueryRow(
		ctx,
		`SELECT id, workspace_id, table_id, uploader_type, uploader_id,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status, created_at
		 FROM generic_table_attachment
		 WHERE id = $1 AND workspace_id = $2 AND table_id = $3
		 FOR UPDATE`,
		attachmentID, wsUUID, tableUUID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, false, http.StatusNotFound, "table not found", ""
	}
	if err != nil {
		return rec, false, http.StatusInternalServerError, "database error", ""
	}

	if locked.ContentSHA256 != contentSHA256 || locked.SizeBytes != sizeBytes {
		return rec, false, http.StatusConflict, "idempotency key already used with different file content", "idempotency_conflict"
	}
	if locked.Status == "ready" {
		if err := tx.Commit(ctx); err != nil {
			return rec, false, http.StatusInternalServerError, "database error", ""
		}
		return locked, true, 0, "", ""
	}

	targetID := uuidToString(locked.ID)
	link, err := h.Storage.Upload(ctx, locked.StorageKey, data, locked.ContentType, locked.Filename)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("table attachment upload failed", "table_id", uuidToString(tableUUID), "attachment_id", targetID, "error", err)
		}
		return rec, false, http.StatusInternalServerError, "upload failed", ""
	}

	derivedKey, keyOK := storage.ManagedWorkspaceUploadKey(h.Storage, workspaceID, targetID, link)
	if !keyOK || derivedKey != locked.StorageKey {
		slog.Error("table attachment upload returned unexpected storage url", "attachment_id", targetID, "key", locked.StorageKey)
		return rec, false, http.StatusInternalServerError, "upload failed", ""
	}

	finalized, err := scanTableAttachment(tx.QueryRow(
		ctx,
		`UPDATE generic_table_attachment
		 SET status = 'ready',
		     object_url = $1,
		     ready_at = COALESCE(ready_at, now())
		 WHERE id = $2 AND workspace_id = $3 AND table_id = $4
		 RETURNING id, workspace_id, table_id, uploader_type, uploader_id,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status, created_at`,
		link, locked.ID, wsUUID, tableUUID,
	))
	if err != nil {
		slog.Error("failed to finalize table attachment metadata", "attachment_id", targetID, "error", err)
		return rec, false, http.StatusInternalServerError, "failed to save attachment metadata", ""
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("failed to commit table attachment metadata", "attachment_id", targetID, "error", err)
		return rec, false, http.StatusInternalServerError, "failed to save attachment metadata", ""
	}
	return finalized, false, 0, "", ""
}

// ListTableAttachments — GET /api/tables/{tableId}/attachments
func (h *Handler) ListTableAttachments(w http.ResponseWriter, r *http.Request) {
	_, _, wsUUID, tableUUID, ok := h.requireTableAttachmentScope(w, r)
	if !ok {
		return
	}

	rows, err := h.DB.Query(
		r.Context(),
		`SELECT id, workspace_id, table_id, uploader_type, uploader_id,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status, created_at
		 FROM generic_table_attachment
		 WHERE workspace_id = $1 AND table_id = $2 AND status = 'ready'
		 ORDER BY created_at DESC, id DESC`,
		wsUUID, tableUUID,
	)
	if err != nil {
		slog.Error("failed to list table attachments", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list attachments")
		return
	}
	defer rows.Close()

	attachments := make([]TableAttachmentResponse, 0)
	for rows.Next() {
		rec, err := scanTableAttachment(rows)
		if err != nil {
			slog.Error("failed to scan table attachment", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list attachments")
			return
		}
		attachments = append(attachments, tableAttachmentToResponse(rec))
	}
	if err := rows.Err(); err != nil {
		slog.Error("failed to iterate table attachments", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list attachments")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"attachments": attachments,
	})
}

func (h *Handler) loadReadyTableAttachment(w http.ResponseWriter, r *http.Request) (workspaceID string, rec tableAttachmentRecord, ok bool) {
	_, workspaceID, wsUUID, tableUUID, ok := h.requireTableAttachmentScope(w, r)
	if !ok {
		return "", rec, false
	}
	attUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "attachmentId"), "attachment_id")
	if !ok {
		return "", rec, false
	}
	rec, err := scanTableAttachment(h.DB.QueryRow(
		r.Context(),
		`SELECT id, workspace_id, table_id, uploader_type, uploader_id,
			filename, content_type, size_bytes, content_sha256, storage_key, object_url, status, created_at
		 FROM generic_table_attachment
		 WHERE id = $1 AND workspace_id = $2 AND table_id = $3 AND status = 'ready'`,
		attUUID, wsUUID, tableUUID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "attachment not found")
		return "", rec, false
	}
	if err != nil {
		slog.Error("failed to load table attachment", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load attachment")
		return "", rec, false
	}
	return workspaceID, rec, true
}

// GetTableAttachment — GET /api/tables/{tableId}/attachments/{attachmentId}
func (h *Handler) GetTableAttachment(w http.ResponseWriter, r *http.Request) {
	_, rec, ok := h.loadReadyTableAttachment(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attachment": tableAttachmentToResponse(rec),
	})
}

// DownloadTableAttachmentContent — GET /api/tables/{tableId}/attachments/{attachmentId}/content
func (h *Handler) DownloadTableAttachmentContent(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeFeatureDisabled(w, "file_upload_not_configured", "file upload not configured")
		return
	}
	workspaceID, rec, ok := h.loadReadyTableAttachment(w, r)
	if !ok {
		return
	}
	attID := uuidToString(rec.ID)
	key, keyOK := storage.ManagedWorkspaceUploadKey(h.Storage, workspaceID, attID, rec.ObjectURL)
	if !keyOK || key != rec.StorageKey {
		slog.Error("table attachment storage key mismatch", "attachment_id", attID)
		writeError(w, http.StatusNotFound, "attachment object not found")
		return
	}
	h.proxyAttachmentDownload(w, r, db.Attachment{
		ID:          rec.ID,
		Filename:    rec.Filename,
		ContentType: rec.ContentType,
		SizeBytes:   rec.SizeBytes,
	}, key, true)
}
