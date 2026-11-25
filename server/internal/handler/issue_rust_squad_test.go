package handler

import (
	"context"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestSquadLeaderIssueOriginatorMember(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	issue := db.Issue{}
	if got := h.squadLeaderIssueOriginator(context.Background(), "member", "user-1", issue); got != "user-1" {
		t.Fatalf("member originator = %q, want user-1", got)
	}
}
