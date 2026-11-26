package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIsGenericTableStructuralDelete(t *testing.T) {
	t.Parallel()

	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodDelete, "/api/tables/11111111-1111-4111-8111-111111111111", true},
		{http.MethodDelete, "/api/tables/11111111-1111-4111-8111-111111111111/", true},
		{http.MethodDelete, "/api/tables/11111111-1111-4111-8111-111111111111/fields/22222222-2222-4222-8222-222222222222", true},
		{http.MethodDelete, "/api/tables/11111111-1111-4111-8111-111111111111/views/55555555-5555-4555-8555-555555555555", true},
		{http.MethodDelete, "/api/tables/11111111-1111-4111-8111-111111111111/records/33333333-3333-4333-8333-333333333333", false},
		{http.MethodGet, "/api/tables/11111111-1111-4111-8111-111111111111", false},
		{http.MethodPatch, "/api/tables/11111111-1111-4111-8111-111111111111/fields/22222222-2222-4222-8222-222222222222", false},
		{http.MethodPost, "/api/tables/11111111-1111-4111-8111-111111111111/views", false},
		{http.MethodDelete, "/api/tables", false},
		{http.MethodDelete, "/api/tables/", false},
	}

	for _, tc := range cases {
		if got := isGenericTableStructuralDelete(tc.method, tc.path); got != tc.want {
			t.Errorf("isGenericTableStructuralDelete(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

type fakeTableTaskAuthStore struct {
	task       db.AgentTaskQueue
	taskErr    error
	members    map[string]db.Member
	memberErr  error
	gotTaskArg db.GetAgentTaskInWorkspaceParams
}

func (f *fakeTableTaskAuthStore) GetAgentTaskInWorkspace(_ context.Context, arg db.GetAgentTaskInWorkspaceParams) (db.AgentTaskQueue, error) {
	f.gotTaskArg = arg
	if f.taskErr != nil {
		return db.AgentTaskQueue{}, f.taskErr
	}
	return f.task, nil
}

func (f *fakeTableTaskAuthStore) GetMemberByUserAndWorkspace(_ context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error) {
	if f.memberErr != nil {
		return db.Member{}, f.memberErr
	}
	key := util.UUIDToString(arg.WorkspaceID) + ":" + util.UUIDToString(arg.UserID)
	m, ok := f.members[key]
	if !ok {
		return db.Member{}, errors.New("not found")
	}
	return m, nil
}

func mustTestUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := util.ParseUUID(s)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", s, err)
	}
	return id
}

func TestRequireGenericTableStructuralDeleteAccess(t *testing.T) {
	t.Parallel()

	const (
		wsID       = "11111111-1111-4111-8111-111111111111"
		taskID     = "22222222-2222-4222-8222-222222222222"
		agentID    = "33333333-3333-4333-8333-333333333333"
		otherAgent = "44444444-4444-4444-8444-444444444444"
		adminUser  = "55555555-5555-4555-8555-555555555555"
		memberUser = "66666666-6666-4666-8666-666666666666"
		ownerUser  = "77777777-7777-4777-8777-777777777777"
	)

	baseMembers := map[string]db.Member{
		wsID + ":" + adminUser:  {Role: "admin"},
		wsID + ":" + ownerUser:  {Role: "owner"},
		wsID + ":" + memberUser: {Role: "member"},
	}

	makeReq := func(actorSource, reqWS, reqTask, reqAgent string) *http.Request {
		req := httptest.NewRequest(http.MethodDelete, "/api/tables/"+wsID, nil)
		if reqWS != "" {
			req = req.WithContext(middleware.SetMemberContext(req.Context(), reqWS, db.Member{Role: "admin"}))
		}
		if actorSource != "" {
			req.Header.Set("X-Actor-Source", actorSource)
		}
		if reqTask != "" {
			req.Header.Set("X-Task-ID", reqTask)
		}
		if reqAgent != "" {
			req.Header.Set("X-Agent-ID", reqAgent)
		}
		return req
	}

	cases := []struct {
		name       string
		store      *fakeTableTaskAuthStore
		req        *http.Request
		wantStatus int
	}{
		{
			name:       "human member request passes through to RequireWorkspaceRole",
			store:      &fakeTableTaskAuthStore{},
			req:        makeReq("", wsID, "", ""),
			wantStatus: http.StatusOK,
		},
		{
			name: "task_token with admin originator allowed",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:               mustTestUUID(t, taskID),
					AgentID:          mustTestUUID(t, agentID),
					OriginatorUserID: mustTestUUID(t, adminUser),
				},
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusOK,
		},
		{
			name: "task_token with legacy accountable owner fallback allowed",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:                mustTestUUID(t, taskID),
					AgentID:           mustTestUUID(t, agentID),
					AccountableUserID: mustTestUUID(t, ownerUser),
				},
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusOK,
		},
		{
			name: "task_token with non-admin originator blocked even on admin daemon",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:               mustTestUUID(t, taskID),
					AgentID:          mustTestUUID(t, agentID),
					OriginatorUserID: mustTestUUID(t, memberUser),
				},
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "task_token with legacy accountable member blocked",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:                mustTestUUID(t, taskID),
					AgentID:           mustTestUUID(t, agentID),
					AccountableUserID: mustTestUUID(t, memberUser),
				},
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "task_token with unattributed task fails closed",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:      mustTestUUID(t, taskID),
					AgentID: mustTestUUID(t, agentID),
				},
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "task_token with mismatched agent id fails closed",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:               mustTestUUID(t, taskID),
					AgentID:          mustTestUUID(t, otherAgent),
					OriginatorUserID: mustTestUUID(t, adminUser),
				},
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "task_token missing task id header fails closed",
			store: &fakeTableTaskAuthStore{
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, "", agentID),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "task_token task not in workspace fails closed",
			store: &fakeTableTaskAuthStore{
				taskErr: errors.New("no rows"),
				members: baseMembers,
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "task_token originator no longer member fails closed",
			store: &fakeTableTaskAuthStore{
				task: db.AgentTaskQueue{
					ID:               mustTestUUID(t, taskID),
					AgentID:          mustTestUUID(t, agentID),
					OriginatorUserID: mustTestUUID(t, adminUser),
				},
				members: map[string]db.Member{},
			},
			req:        makeReq("task_token", wsID, taskID, agentID),
			wantStatus: http.StatusForbidden,
		},
	}

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mw := requireGenericTableStructuralDeleteAccess(tc.store)
			rec := httptest.NewRecorder()
			mw(okHandler).ServeHTTP(rec, tc.req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}
