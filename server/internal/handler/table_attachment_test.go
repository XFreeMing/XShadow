package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/storage"
)

func buildTableAttachmentRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/tables/{tableId}/attachments", func(r chi.Router) {
		r.Get("/", h.ListTableAttachments)
		r.Post("/", h.UploadTableAttachment)
		r.Get("/{attachmentId}", h.GetTableAttachment)
		r.Get("/{attachmentId}/content", h.DownloadTableAttachmentContent)
	})
	return r
}

func makeTableAttachmentUploadReq(t *testing.T, tableID, workspaceID, userID, idemKey, filename string, payload []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tables/"+tableID+"/attachments", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	return req
}

func countFilesInDir(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !strings.HasSuffix(d.Name(), ".meta.json") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return count
}

func TestTableAttachmentsRoundTripIdempotencyAndIsolation(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", storeDir)
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "http://localhost:8080")
	localStore := storage.NewLocalStorageFromEnv()
	if localStore == nil {
		t.Fatalf("NewLocalStorageFromEnv returned nil")
	}

	h := newTestHandler(Config{})
	h.Storage = localStore
	router := buildTableAttachmentRouter(h)

	// Workspace 1 (testWorkspaceID) with two tables: tableA and tableB.
	var tableA, tableB string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO generic_table (workspace_id, name) VALUES ($1::uuid, 'Table A') RETURNING id::text`,
		testWorkspaceID,
	).Scan(&tableA); err != nil {
		t.Fatalf("create tableA: %v", err)
	}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO generic_table (workspace_id, name) VALUES ($1::uuid, 'Table B') RETURNING id::text`,
		testWorkspaceID,
	).Scan(&tableB); err != nil {
		t.Fatalf("create tableB: %v", err)
	}

	// Workspace 2 with a separate owner and tableC.
	ws2ID := uuid.NewString()
	user2ID := uuid.NewString()
	if _, err := testPool.Exec(ctx,
		`INSERT INTO "user" (id, name, email) VALUES ($1::uuid, 'Other User', $2)`,
		user2ID, "other-"+user2ID[:8]+"@example.com",
	); err != nil {
		t.Fatalf("create user2: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO workspace (id, name, slug) VALUES ($1::uuid, 'Other WS', $2)`,
		ws2ID, "other-ws-"+ws2ID[:8],
	); err != nil {
		t.Fatalf("create ws2: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'owner')`,
		ws2ID, user2ID,
	); err != nil {
		t.Fatalf("create ws2 member: %v", err)
	}
	var tableC string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO generic_table (workspace_id, name) VALUES ($1::uuid, 'Table C') RETURNING id::text`,
		ws2ID,
	).Scan(&tableC); err != nil {
		t.Fatalf("create tableC: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM generic_table_attachment WHERE table_id IN ($1::uuid, $2::uuid, $3::uuid)`, tableA, tableB, tableC)
		_, _ = testPool.Exec(ctx, `DELETE FROM generic_table WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, tableA, tableB, tableC)
		_, _ = testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1::uuid`, ws2ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1::uuid`, ws2ID)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1::uuid`, user2ID)
	})

	// Missing Idempotency-Key returns 400.
	badReq := makeTableAttachmentUploadReq(t, tableA, testWorkspaceID, testUserID, "", "note.txt", []byte("hello"))
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key status = %d, want 400", badRec.Code)
	}

	// Upload bytes to tableA with an Idempotency-Key.
	idemKey := uuid.NewString()
	payload := []byte("hello generic table attachment bytes\n")
	upReq := makeTableAttachmentUploadReq(t, tableA, testWorkspaceID, testUserID, idemKey, "report.txt", payload)
	upRec := httptest.NewRecorder()
	router.ServeHTTP(upRec, upReq)
	if upRec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", upRec.Code, upRec.Body.String())
	}

	var upBody struct {
		Attachment map[string]any `json:"attachment"`
	}
	if err := json.Unmarshal(upRec.Body.Bytes(), &upBody); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	attID, _ := upBody.Attachment["id"].(string)
	if _, err := uuid.Parse(attID); err != nil {
		t.Fatalf("invalid attachment id %q", attID)
	}
	if _, hasURL := upBody.Attachment["url"]; hasURL {
		t.Fatalf("response must not expose raw url field: %v", upBody.Attachment)
	}
	if _, hasObjURL := upBody.Attachment["object_url"]; hasObjURL {
		t.Fatalf("response must not expose object_url field: %v", upBody.Attachment)
	}
	if _, hasKey := upBody.Attachment["storage_key"]; hasKey {
		t.Fatalf("response must not expose storage_key field: %v", upBody.Attachment)
	}
	wantDownloadPath := "/api/tables/" + tableA + "/attachments/" + attID + "/content"
	if got := upBody.Attachment["download_path"]; got != wantDownloadPath {
		t.Fatalf("download_path = %v, want %q", got, wantDownloadPath)
	}
	if files := countFilesInDir(t, storeDir); files != 1 {
		t.Fatalf("stored files = %d, want 1", files)
	}

	// Identical retry returns the same attachment without creating duplicate objects.
	replayReq := makeTableAttachmentUploadReq(t, tableA, testWorkspaceID, testUserID, idemKey, "report.txt", payload)
	replayRec := httptest.NewRecorder()
	router.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body = %s", replayRec.Code, replayRec.Body.String())
	}
	var replayBody struct {
		Attachment TableAttachmentResponse `json:"attachment"`
	}
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replayBody); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayBody.Attachment.ID != attID {
		t.Fatalf("replay id = %q, want %q", replayBody.Attachment.ID, attID)
	}
	if files := countFilesInDir(t, storeDir); files != 1 {
		t.Fatalf("stored files after replay = %d, want 1", files)
	}

	// Reusing the same Idempotency-Key with different bytes returns 409 and no new object.
	conflictReq := makeTableAttachmentUploadReq(t, tableA, testWorkspaceID, testUserID, idemKey, "report.txt", []byte("different bytes"))
	conflictRec := httptest.NewRecorder()
	router.ServeHTTP(conflictRec, conflictReq)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409, body = %s", conflictRec.Code, conflictRec.Body.String())
	}
	if files := countFilesInDir(t, storeDir); files != 1 {
		t.Fatalf("stored files after conflict = %d, want 1", files)
	}

	// GET list and GET metadata on tableA succeed.
	listReq := httptest.NewRequest(http.MethodGet, "/api/tables/"+tableA+"/attachments", nil)
	listReq.Header.Set("X-User-ID", testUserID)
	listReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), attID) {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	metaReq := httptest.NewRequest(http.MethodGet, "/api/tables/"+tableA+"/attachments/"+attID, nil)
	metaReq.Header.Set("X-User-ID", testUserID)
	metaReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	metaRec := httptest.NewRecorder()
	router.ServeHTTP(metaRec, metaReq)
	if metaRec.Code != http.StatusOK || !strings.Contains(metaRec.Body.String(), attID) {
		t.Fatalf("get metadata status = %d, body = %s", metaRec.Code, metaRec.Body.String())
	}

	// GET content streams exact bytes with attachment disposition + nosniff.
	dlReq := httptest.NewRequest(http.MethodGet, wantDownloadPath, nil)
	dlReq.Header.Set("X-User-ID", testUserID)
	dlReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	dlRec := httptest.NewRecorder()
	router.ServeHTTP(dlRec, dlReq)
	if dlRec.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", dlRec.Code, dlRec.Body.String())
	}
	gotBytes, _ := io.ReadAll(dlRec.Body)
	if !bytes.Equal(gotBytes, payload) {
		t.Fatalf("downloaded bytes = %q, want %q", gotBytes, payload)
	}
	if cd := dlRec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Fatalf("Content-Disposition = %q, want attachment; ...", cd)
	}
	if nosniff := dlRec.Header().Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}

	// Cross-table isolation: tableB cannot GET or download tableA's attachment.
	for _, path := range []string{
		"/api/tables/" + tableB + "/attachments/" + attID,
		"/api/tables/" + tableB + "/attachments/" + attID + "/content",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-table %s status = %d, want 404", path, rec.Code)
		}
	}

	// Cross-workspace isolation: ws2 user cannot access ws1 tableA (403 on ws1, 404 on ws2).
	wsMismatchReq := httptest.NewRequest(http.MethodGet, "/api/tables/"+tableA+"/attachments/"+attID, nil)
	wsMismatchReq.Header.Set("X-User-ID", user2ID)
	wsMismatchReq.Header.Set("X-Workspace-ID", ws2ID)
	wsMismatchRec := httptest.NewRecorder()
	router.ServeHTTP(wsMismatchRec, wsMismatchReq)
	if wsMismatchRec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace tableA in ws2 status = %d, want 404", wsMismatchRec.Code)
	}

	nonMemberReq := httptest.NewRequest(http.MethodGet, "/api/tables/"+tableA+"/attachments/"+attID, nil)
	nonMemberReq.Header.Set("X-User-ID", user2ID)
	nonMemberReq.Header.Set("X-Workspace-ID", testWorkspaceID)
	nonMemberRec := httptest.NewRecorder()
	router.ServeHTTP(nonMemberRec, nonMemberReq)
	if nonMemberRec.Code != http.StatusForbidden {
		t.Fatalf("non-member ws1 access status = %d, want 403", nonMemberRec.Code)
	}

	// Concurrent identical uploads with the same Idempotency-Key serialize on the
	// row lock, produce identical attachment IDs, and write only one object.
	concKey := uuid.NewString()
	concPayload := []byte("concurrent identical bytes")
	var wg sync.WaitGroup
	ids := make([]string, 2)
	codes := make([]int, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := makeTableAttachmentUploadReq(t, tableA, testWorkspaceID, testUserID, concKey, "conc.txt", concPayload)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			codes[idx] = rec.Code
			var body struct {
				Attachment TableAttachmentResponse `json:"attachment"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			ids[idx] = body.Attachment.ID
		}(i)
	}
	wg.Wait()
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("concurrent identical uploads produced mismatched ids: %v (codes=%v)", ids, codes)
	}
	if files := countFilesInDir(t, storeDir); files != 2 {
		t.Fatalf("stored files after concurrent identical upload = %d, want 2", files)
	}
}

func TestUploadTableAttachmentFailsSafelyWithoutTxStarter(t *testing.T) {
	h := &Handler{Storage: &mockStorage{}}
	router := buildTableAttachmentRouter(h)
	req := makeTableAttachmentUploadReq(t, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), "a.txt", []byte("hi"))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("nil TxStarter status = %d, want 500", rec.Code)
	}
}
