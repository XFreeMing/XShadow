package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// A dependency-parked task is readable from the ordinary task API. `fire_at`
// is present and null (no timer will resume it), and `waiting` carries the
// persisted reason and the real release event class. A timed deferred task is
// distinguishable: it has a fire_at and no waiting block.
func TestAgentTaskResponseExposesDependencyWait(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "dependency wait runtime")
	agentID := dbfx.Agent(t, "dependency wait agent", runtimeID)
	parkedID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"status":     "deferred",
		"context": testutil.Raw(`'{"dependency_wait":{
			"reason":"dependency_not_accepted",
			"detail":"prerequisite 018f3a9e-3333-7c2b-9f1e-000000000001 has no currently valid acceptance for its delivery",
			"event":"dependency_changed",
			"prerequisites":[{"item_id":"node-a","issue_id":"018f3a9e-3333-7c2b-9f1e-000000000001","status":"done","reason":"dependency_not_accepted"}]}}'::jsonb`),
	})
	timedID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"status":     "deferred",
		"fire_at":    testutil.Raw("now() + interval '5 minutes'"),
		"context": testutil.Raw(`'{"dependency_wait":{
			"reason":"dependency_not_accepted","detail":"stale","event":"dependency_changed"}}'::jsonb`),
	})

	req := withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID+"/tasks", nil), "id", agentID)
	response := testutil.Call(t, testHandler.ListAgentTasks, req).Want(http.StatusOK)
	var raw []map[string]any
	response.JSON(&raw)
	byID := map[string]map[string]any{}
	for _, task := range raw {
		if id, ok := task["id"].(string); ok {
			byID[id] = task
		}
	}

	parked, ok := byID[parkedID]
	if !ok {
		t.Fatalf("parked task %s missing from response", parkedID)
	}
	fireAt, present := parked["fire_at"]
	if !present {
		t.Fatal("fire_at field is absent; deferred + null must be explicit")
	}
	if fireAt != nil {
		t.Fatalf("parked fire_at = %v, want null", fireAt)
	}
	waiting, ok := parked["waiting"].(map[string]any)
	if !ok {
		t.Fatalf("parked waiting = %v, want an object", parked["waiting"])
	}
	if waiting["reason"] != "dependency_not_accepted" || waiting["event"] != "dependency_changed" {
		t.Fatalf("parked waiting = %v", waiting)
	}
	if waiting["detail"] == "" {
		t.Fatal("parked waiting detail is empty")
	}
	// Structured facts let the client localize from the stable reason instead
	// of rendering the English detail as the primary prompt.
	prerequisites, ok := waiting["prerequisites"].([]any)
	if !ok || len(prerequisites) != 1 {
		t.Fatalf("parked waiting prerequisites = %v, want one", waiting["prerequisites"])
	}
	prerequisite, ok := prerequisites[0].(map[string]any)
	if !ok {
		t.Fatalf("parked prerequisite = %v", prerequisites[0])
	}
	if prerequisite["item_id"] != "node-a" ||
		prerequisite["issue_id"] != "018f3a9e-3333-7c2b-9f1e-000000000001" ||
		prerequisite["status"] != "done" ||
		prerequisite["reason"] != "dependency_not_accepted" {
		t.Fatalf("parked prerequisite = %v", prerequisite)
	}

	timed, ok := byID[timedID]
	if !ok {
		t.Fatalf("timed task %s missing from response", timedID)
	}
	if timed["fire_at"] == nil {
		t.Fatal("timed deferred task lost its fire_at")
	}
	if _, present := timed["waiting"]; present {
		t.Fatalf("timed deferred task reported a dependency wait: %v", timed["waiting"])
	}
}
