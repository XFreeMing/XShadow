package handler

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

type createHandoff struct {
	assigneeType string
	assigneeID   pgtype.UUID
	methodOwns   bool
}

// A draft cannot have a formal goal link yet. The preview and the eventual
// create must use the same deliverable and the same workspace method catalog.
func (h *Handler) createHandoffAssignee(ctx context.Context, project db.Project, step issueworkflow.Step, title, description, creatorType string, creatorID pgtype.UUID) (createHandoff, error) {
	methods, err := h.workMethodCatalog(ctx, project.WorkspaceID)
	if err != nil {
		return createHandoff{}, err
	}
	run := workmethod.Recommend(methods, title, description, "")
	if methodOwnsHandoff(run) {
		return createHandoff{methodOwns: true}, nil
	}
	kind, id, ok := issueworkflow.ResolveHandler(step, project, creatorType, creatorID)
	if !ok {
		return createHandoff{}, nil
	}
	return createHandoff{assigneeType: kind, assigneeID: id}, nil
}
