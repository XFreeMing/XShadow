package storage

import "testing"

func TestScopedWorkspaceAttachmentKeyValid(t *testing.T) {
	ws := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	att := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if !ScopedWorkspaceAttachmentKeyValid(ws, att, "workspaces/"+ws+"/"+att+".png") {
		t.Fatal("expected simple extension key")
	}
	if ScopedWorkspaceAttachmentKeyValid(ws, att, "workspaces/"+ws+"/"+att+".tar.gz") {
		t.Fatal("expected multi-dot extension to fail")
	}
}

func TestScopedSourceContextCloneKeyValid(t *testing.T) {
	ws := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	clone := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if !ScopedSourceContextCloneKeyValid(ws, clone, "workspaces/"+ws+"/source-context/"+clone+".txt") {
		t.Fatal("expected clone extension key")
	}
	if ScopedSourceContextCloneKeyValid(ws, clone, "workspaces/"+ws+"/"+clone+".txt") {
		t.Fatal("expected direct upload key to fail")
	}
}
