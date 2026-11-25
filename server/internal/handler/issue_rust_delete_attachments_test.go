package handler

import (
	"context"
	"io"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
)

type roundTripStorage struct {
	baseURL string
}

func (s *roundTripStorage) Upload(ctx context.Context, key string, data []byte, contentType, filename string) (string, error) {
	return s.ObjectURL(key), nil
}
func (s *roundTripStorage) Delete(ctx context.Context, key string)             {}
func (s *roundTripStorage) DeleteObject(ctx context.Context, key string) error { return nil }
func (s *roundTripStorage) DeleteKeys(ctx context.Context, keys []string)      {}
func (s *roundTripStorage) KeyFromURL(rawURL string) string {
	const marker = "/uploads/"
	if i := len(rawURL); i > 0 {
		for j := 0; j+len(marker) <= len(rawURL); j++ {
			if rawURL[j:j+len(marker)] == marker {
				return rawURL[j+len(marker):]
			}
		}
	}
	return ""
}
func (s *roundTripStorage) ObjectURL(key string) string { return s.baseURL + "/uploads/" + key }
func (s *roundTripStorage) CdnDomain() string           { return "" }
func (s *roundTripStorage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, nil
}

func TestManagedWorkspaceUploadKeyRoundTrip(t *testing.T) {
	store := &roundTripStorage{baseURL: "http://127.0.0.1:18081"}
	ws := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	att := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	key := "workspaces/" + ws + "/" + att + ".png"
	url := store.ObjectURL(key)
	got, ok := storage.ManagedWorkspaceUploadKey(store, ws, att, url)
	if !ok || got != key {
		t.Fatalf("key=%q ok=%v", got, ok)
	}
}

func TestRustIssueDeleteAttachmentIDsMatchPlan(t *testing.T) {
	plan := []rustIssueDeleteAttachmentPlanItem{
		{AttachmentID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
		{AttachmentID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"},
	}
	if !rustIssueDeleteAttachmentIDsMatchPlan(plan, []string{plan[1].AttachmentID, plan[0].AttachmentID}) {
		t.Fatal("expected order-independent id match")
	}
	if rustIssueDeleteAttachmentIDsMatchPlan(plan, []string{"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}) {
		t.Fatal("expected foreign id mismatch")
	}
}
