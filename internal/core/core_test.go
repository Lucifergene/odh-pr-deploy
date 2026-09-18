package core

import "testing"

func TestGenAIStackUsesSamePRSHAForHostAndRemote(t *testing.T) {
	stack := GenAIStackFromSHA("abc123")
	if len(stack) != 2 {
		t.Fatalf("got %d images, want 2", len(stack))
	}
	if stack[0].Image != "quay.io/opendatahub/odh-mod-arch-gen-ai:odh-pr-abc123" || stack[1].Image != "quay.io/opendatahub/odh-dashboard:odh-pr-abc123" {
		t.Fatalf("unexpected stack: %#v", stack)
	}
}
func TestCustomStackRejectsUnpairedGenAIImage(t *testing.T) {
	if _, err := CustomGenAIStack("quay.io/example/genai:pr", ""); err == nil {
		t.Fatal("expected unpaired image error")
	}
}
