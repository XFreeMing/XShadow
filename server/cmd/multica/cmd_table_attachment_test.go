package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTableAttachmentCLIRoundTripAndGuards(t *testing.T) {
	const attID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const idemKey = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	wantBytes := []byte("table-attachment-cli-bytes\n")

	var uploadCalls atomic.Int32
	var downloadCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mat_table_test_token" {
			t.Errorf("Authorization = %q, want task token", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/attachments":
			uploadCalls.Add(1)
			if got := r.Header.Get("Idempotency-Key"); got != idemKey {
				t.Errorf("Idempotency-Key = %q, want %q", got, idemKey)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Errorf("FormFile: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			gotData, _ := io.ReadAll(file)
			if header.Filename != "sample.txt" || !bytes.Equal(gotData, wantBytes) {
				t.Errorf("uploaded (%q, %q), want (sample.txt, %q)", header.Filename, gotData, wantBytes)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"attachment":{"id":"`+attID+`","filename":"sample.txt","content_type":"text/plain; charset=utf-8","size_bytes":27,"download_path":"/api/tables/`+testTableID1+`/attachments/`+attID+`/content"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/attachments":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"attachments":[{"id":"`+attID+`","filename":"sample.txt"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/attachments/"+attID:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"attachment":{"id":"`+attID+`","filename":"sample.txt","download_path":"/api/tables/`+testTableID1+`/attachments/`+attID+`/content"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/attachments/"+attID+"/content":
			downloadCalls.Add(1)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="sample.txt"`)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(wantBytes)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	outer := t.TempDir()
	outsideFile := filepath.Join(outer, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(outer, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)

	srcPath := filepath.Join(workDir, "sample.txt")
	if err := os.WriteFile(srcPath, wantBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Upload inside workdir succeeds.
	out, err := runRootTableCommand(t, "", "table", "attachment", "upload", testTableID1, "--file", "sample.txt", "--key", idemKey)
	if err != nil || !strings.Contains(out, attID) {
		t.Fatalf("table attachment upload = (%q, %v)", out, err)
	}
	if uploadCalls.Load() != 1 {
		t.Fatalf("uploadCalls = %d, want 1", uploadCalls.Load())
	}

	// List & get succeed.
	if out, err := runRootTableCommand(t, "", "table", "attachment", "list", testTableID1); err != nil || !strings.Contains(out, attID) {
		t.Fatalf("table attachment list = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "attachment", "get", testTableID1, attID); err != nil || !strings.Contains(out, attID) {
		t.Fatalf("table attachment get = (%q, %v)", out, err)
	}

	// Download to nested path inside workdir succeeds and writes exact bytes.
	out, err = runRootTableCommand(t, "", "table", "attachment", "download", testTableID1, attID, "--file", "downloads/saved.txt")
	if err != nil || !strings.Contains(out, attID) {
		t.Fatalf("table attachment download = (%q, %v)", out, err)
	}
	if downloadCalls.Load() != 1 {
		t.Fatalf("downloadCalls = %d, want 1", downloadCalls.Load())
	}
	savedBytes, err := os.ReadFile(filepath.Join(workDir, "downloads", "saved.txt"))
	if err != nil || !bytes.Equal(savedBytes, wantBytes) {
		t.Fatalf("saved bytes = (%q, %v), want %q", savedBytes, err, wantBytes)
	}

	// Workdir guards reject external paths before hitting network.
	if _, err := runRootTableCommand(t, "", "table", "attachment", "upload", testTableID1, "--file", outsideFile, "--key", idemKey); err == nil {
		t.Fatalf("expected upload --file outside workdir to fail")
	}
	if _, err := runRootTableCommand(t, "", "table", "attachment", "download", testTableID1, attID, "--file", filepath.Join(outer, "escaped.txt")); err == nil {
		t.Fatalf("expected download --file outside workdir to fail")
	}
	if uploadCalls.Load() != 1 || downloadCalls.Load() != 1 {
		t.Fatalf("guarded commands should not have hit network: upload=%d download=%d", uploadCalls.Load(), downloadCalls.Load())
	}
}
