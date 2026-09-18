package core

import "testing"

func TestComponentForDashboardUsesOperatorOverride(t *testing.T) {
	c, err := ComponentFor("dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if c.ImageEnv != "RELATED_IMAGE_ODH_DASHBOARD_IMAGE" || c.Deployment != "rhods-dashboard" {
		t.Fatalf("unexpected mapping: %#v", c)
	}
}
func TestNewSessionIDIsSafeForStatePath(t *testing.T) {
	id, err := NewSessionID("gen-ai")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidSessionID(id) {
		t.Fatalf("unsafe generated ID %q", id)
	}
	if ValidSessionID("../escape") {
		t.Fatal("path traversal accepted")
	}
}
