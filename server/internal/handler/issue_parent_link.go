package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issueparentlink"
)

func parentIssueUpdateOnly(rawFields map[string]json.RawMessage, attachmentIDs []pgtype.UUID) bool {
	if len(attachmentIDs) > 0 {
		return false
	}
	if _, ok := rawFields["parent_issue_id"]; !ok {
		return false
	}
	for key := range rawFields {
		switch key {
		case "parent_issue_id", "expected_revision", "suppress_run":
		default:
			return false
		}
	}
	return true
}

func isIssueParentLinkRejection(err error) bool {
	return errors.Is(err, issueparentlink.ErrCircular) ||
		errors.Is(err, issueparentlink.ErrSelfParent) ||
		errors.Is(err, issueparentlink.ErrParentNotFound)
}

func writeIssueParentLinkError(w http.ResponseWriter, err error) bool {
	if !isIssueParentLinkRejection(err) {
		return false
	}
	msg := err.Error()
	if errors.Is(err, issueparentlink.ErrParentNotFound) {
		msg = "parent issue not found in this workspace"
	}
	writeError(w, http.StatusBadRequest, msg)
	return true
}
