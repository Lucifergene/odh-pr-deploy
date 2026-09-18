package app

import (
	"context"
	"strings"
	"testing"

	"github.com/akundu/odh-pr-deploy/internal/core"
)

type fakeRunner struct {
	calls []string
	data  map[string]string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	for key, value := range r.data {
		if strings.Contains(call, key) {
			return []byte(value), nil
		}
	}
	return nil, nil
}

func TestShadowDeployScopesAllOCCommandsAndCreatesOwnerlessClone(t *testing.T) {
	runner := &fakeRunner{data: map[string]string{
		"image info":                      `{"digest":"sha256:deadbeef"}`,
		"get deployment gen-ai-ui":        `{"metadata":{"name":"gen-ai-ui","namespace":"ns","labels":{"platform.opendatahub.io/part-of":"dashboard"}},"spec":{"selector":{"matchLabels":{"app":"gen-ai-ui"}},"template":{"metadata":{"labels":{"app":"gen-ai-ui"}},"spec":{"containers":[{"name":"gen-ai-ui","image":"original"}]}}}}`,
		"get clusterversion version":      `{"status":{"desired":{"version":"4.22.13"}}}`,
		"get dashboard default-dashboard": `{"metadata":{"annotations":{"platform.opendatahub.io/version":"3.5.0"}}}`,
	}}
	tool := New(runner, t.TempDir())
	session, err := tool.Deploy(context.Background(), DeployOptions{Context: "ctx", Namespace: "ns", Component: "gen-ai", Image: "quay.io/example:pr", Mode: "shadow"})
	if err != nil {
		t.Fatal(err)
	}
	if session.ShadowDeployment == "" {
		t.Fatal("expected shadow deployment name")
	}
	if session.ClusterVersion != "4.22.13" || session.RHOAIVersion != "3.5.0" {
		t.Fatalf("session did not retain cluster baseline: %#v", session)
	}
	if !contains(runner.calls, "oc --context ctx -n ns apply -f ") {
		t.Fatalf("missing scoped apply: %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "oc ") && !strings.Contains(call, "--context ctx") {
			t.Fatalf("unscoped oc command: %s", call)
		}
	}
}

func TestManagedCleanupRefusesWhenOverrideChanged(t *testing.T) {
	runner := &fakeRunner{data: map[string]string{
		"get deployment gen-ai-ui": `{"spec":{"template":{"spec":{"containers":[{"name":"gen-ai-ui","image":"quay.io/someone-else:pr"}]}}}}`,
	}}
	tool := New(runner, t.TempDir())
	session := Session{ID: "s", Mode: "managed", Context: "ctx", Namespace: "ns", Component: core.Component{Name: "gen-ai", Deployment: "gen-ai-ui", Container: "gen-ai-ui"}, Image: "quay.io/ours:pr", OriginalImage: "original"}
	if err := tool.Save(session); err != nil {
		t.Fatal(err)
	}
	if err := tool.Cleanup(context.Background(), "s"); err == nil || !strings.Contains(err.Error(), "refusing cleanup") {
		t.Fatalf("expected concurrent-change refusal, got %v", err)
	}
	if contains(runner.calls, " patch ") {
		t.Fatalf("cleanup must not patch after concurrent change: %#v", runner.calls)
	}
}

func TestManagedDeployRefusesControllerOwnedWorkload(t *testing.T) {
	runner := &fakeRunner{data: map[string]string{
		"image info":               `{"digest":"sha256:deadbeef"}`,
		"get deployment gen-ai-ui": `{"metadata":{"ownerReferences":[{"controller":true,"kind":"Dashboard"}]},"spec":{"template":{"spec":{"containers":[{"name":"gen-ai-ui","image":"original"}]}}}}`,
	}}
	tool := New(runner, t.TempDir())
	_, err := tool.Deploy(context.Background(), DeployOptions{Context: "ctx", Namespace: "ns", Component: "gen-ai", Image: "quay.io/example:pr", Mode: "managed", AllowManaged: true})
	if err == nil || !strings.Contains(err.Error(), "controller-owned") {
		t.Fatalf("expected controller-owned refusal, got %v", err)
	}
	if contains(runner.calls, " patch ") {
		t.Fatalf("managed mode must not patch controller-owned workloads: %#v", runner.calls)
	}
}

func contains(calls []string, part string) bool {
	for _, call := range calls {
		if strings.Contains(call, part) {
			return true
		}
	}
	return false
}
