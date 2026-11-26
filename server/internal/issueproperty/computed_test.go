package issueproperty

import "testing"

func TestIsComputed(t *testing.T) {
	if !IsComputed("lookup") || !IsComputed("formula") {
		t.Fatal("lookup and formula must be computed")
	}
	if IsComputed("text") || IsComputed("record_relation") {
		t.Fatal("editable types must not be computed")
	}
}