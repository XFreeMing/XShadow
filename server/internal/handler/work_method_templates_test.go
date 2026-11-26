package handler

import (
	"net/http"
	"slices"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestWorkMethodTemplateOverridesAreVersionedAndWorkspaceLocal(t *testing.T) {
	slug := workflowTestSuffix()
	ws := dbfx.Workspace(t, "Methods test", "methods-"+slug)
	dbfx.Member(t, ws, testUserID, "owner")
	request := func(method, workspace, id string, body any) *http.Request {
		path := "/api/work-methods"
		if id != "" {
			path += "/" + id
		}
		req := newRequest(method, path, body)
		req.Header.Set("X-Workspace-ID", workspace)
		if id != "" {
			req = withURLParam(req, "id", id)
		}
		return req
	}
	list := func(workspace string) []workmethod.Template {
		var got struct {
			Methods []workmethod.Template `json:"methods"`
		}
		testutil.Call(t, testHandler.ListWorkMethods, request("GET", workspace, "", nil)).Want(200).JSON(&got)
		return got.Methods
	}
	type update struct {
		workmethod.Template
		BaseRevision int `json:"base_revision"`
	}
	put := func(m workmethod.Template, base int) *testutil.Response {
		return testutil.Call(t, testHandler.UpdateWorkMethod, request("PUT", ws, m.ID, update{m, base}))
	}
	original := list(ws)[0]
	m := original
	m.Name = "Workspace iteration"
	m.Steps[0].Owner = &workmethod.Actor{Kind: "member", ID: testUserID}
	m.Steps[0].Reviewer = &workmethod.Actor{Kind: "creator"}
	var saved workmethod.Template
	put(m, original.Revision).Want(200).JSON(&saved)
	if saved.ID != original.ID || saved.Revision != original.Revision+1 {
		t.Fatal(saved)
	}
	put(m, original.Revision).Want(409)
	got := list(ws)
	if len(got) != 2 || got[0].Name != m.Name || got[0].Steps[0].Owner.ID != testUserID || got[1].ID != "okr" {
		t.Fatal(got)
	}
	old, _ := workmethod.ResolvePlanMethod(workmethod.Builtins(), "", "seven-step")
	put(old, old.Revision).Want(409)
	other := list(testWorkspaceID)
	if other[0].Name != original.Name || other[0].Revision != original.Revision {
		t.Fatal("override escaped workspace")
	}
	m.Name = "Survey handbook"
	var created workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, request("POST", ws, "", m)).Want(201).JSON(&created)
	if created.ID == m.ID || created.Revision != 1 {
		t.Fatal(created)
	}
	created.Name = m.Name + " revised"
	put(created, 1).Want(200)
	created.Name = saved.Name
	put(created, 2).Want(409)
	spurious := dbfx.User(t, "Foreign method reviewer", "method-"+slug+"@example.invalid")
	dbfx.Member(t, testWorkspaceID, spurious, "member")
	m.Steps[0].Reviewer = &workmethod.Actor{Kind: "member", ID: spurious}
	put(m, saved.Revision).Want(400)
	m.Steps[0].Reviewer = &workmethod.Actor{Kind: "creator"}
	m.Name = "Concurrent edit"
	// Two editors saw the same version. Only one write can replace it.
	codes := make(chan int, 2)
	for range 2 {
		go func() { codes <- put(m, saved.Revision).Code }()
	}
	gotCodes := []int{<-codes, <-codes}
	slices.Sort(gotCodes)
	if !slices.Equal(gotCodes, []int{200, 409}) || list(ws)[0].Revision != saved.Revision+1 {
		t.Fatal(gotCodes)
	}
}

func TestPendingRecommendationUsesLatestTemplateUntilStarted(t *testing.T) {
	keyword := "设计访谈" + workflowTestSuffix()
	m := workmethod.Builtins()[1]
	m.Name = keyword
	m.Keywords = []string{keyword}
	var created workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods", m)).Want(201).JSON(&created)
	issue := createIssueForTest(t, map[string]any{"title": keyword})
	get := func() workmethod.Run {
		req := withURLParam(newRequest("GET", "/api/issues/"+issue.ID+"/work-method", nil), "id", issue.ID)
		var r workmethod.Run
		testutil.Call(t, testHandler.GetIssueWorkMethod, req).Want(200).JSON(&r)
		return r
	}
	edit := func(version int, instructions string) {
		created.Steps[0].Instructions = instructions
		req := withURLParam(newRequest("PUT", "/api/work-methods/"+created.ID, struct {
			workmethod.Template
			Base int `json:"base_revision"`
		}{created, version}), "id", created.ID)
		testutil.Call(t, testHandler.UpdateWorkMethod, req).Want(200)
	}
	old := get()
	if old.Method == nil || old.Method.ID != created.ID || old.Method.Revision != 1 {
		t.Fatalf("unexpected admission: %+v", old)
	}
	edit(1, "先查看本周访谈材料")
	before := get()
	if before.Method.Revision != 2 || before.Method.Steps[0].Instructions != "先查看本周访谈材料" {
		t.Fatal(before)
	}
	start := func(methodVersion int) *testutil.Response {
		req := withURLParam(newRequest("POST", "/api/issues/"+issue.ID+"/work-method", map[string]any{
			"action": "start", "method_id": created.ID, "base_revision": 0, "method_revision": methodVersion,
		}), "id", issue.ID)
		return testutil.Call(t, testHandler.ChangeIssueWorkMethod, req)
	}
	start(1).Want(409)
	start(2).Want(200)
	edit(2, "改为异步访谈")
	frozen := get()
	if frozen.State != "working" || frozen.Method.Revision != 2 || frozen.Method.Steps[0].Instructions != "先查看本周访谈材料" {
		t.Fatal(frozen)
	}
}
