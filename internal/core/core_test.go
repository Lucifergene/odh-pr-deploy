package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComponentForGenAI(t *testing.T) {
	component, err := ComponentFor("gen-ai")
	if err != nil {
		t.Fatal(err)
	}
	if component.Deployment != "gen-ai-ui" || component.Container != "gen-ai-ui" {
		t.Fatalf("unexpected component: %#v", component)
	}
	if component.ImageEnv != "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE" {
		t.Fatalf("unexpected image env: %s", component.ImageEnv)
	}
}

func TestPinImageUsesDigestInsteadOfMutableTag(t *testing.T) {
	got, err := PinImage("quay.io/opendatahub/odh-mod-arch-gen-ai:odh-pr-abc", "sha256:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if got != "quay.io/opendatahub/odh-mod-arch-gen-ai@sha256:deadbeef" {
		t.Fatalf("got %q", got)
	}
}

func TestDeploymentImagePatchTargetsOnlySelectedContainer(t *testing.T) {
	patch, err := DeploymentImagePatch("gen-ai-ui", "quay.io/example@sha256:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(patch, &got); err != nil {
		t.Fatal(err)
	}
	container := got["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if container["name"] != "gen-ai-ui" || container["image"] != "quay.io/example@sha256:deadbeef" {
		t.Fatalf("unexpected image patch: %#v", container)
	}
}

func TestRestorePatchRemovesPreviouslyAbsentVariable(t *testing.T) {
	patch, err := RestorePatch("manager", "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE", ImageVariable{}, "quay.io/example:pr")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(patch, &got); err != nil {
		t.Fatal(err)
	}
	env := got["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)[0].(map[string]any)
	if env["name"] != "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE" || env["$patch"] != "delete" {
		t.Fatalf("unexpected restoration env patch: %#v", env)
	}
}

func TestRestorePatchRestoresExistingValue(t *testing.T) {
	patch, err := RestorePatch("manager", "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE", ImageVariable{Present: true, Value: "registry.redhat.io/rhoai/gen-ai@sha256:original"}, "quay.io/example:pr")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(patch, &got); err != nil {
		t.Fatal(err)
	}
	env := got["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"].([]any)[0].(map[string]any)
	if env["value"] != "registry.redhat.io/rhoai/gen-ai@sha256:original" {
		t.Fatalf("unexpected restoration env patch: %#v", env)
	}
}

func TestSessionRoundTripPreservesAbsentVariable(t *testing.T) {
	session := Session{ID: "session-1", OriginalVariable: ImageVariable{}, OriginalImage: "original"}
	data, err := MarshalSession(session)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalSession(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.OriginalVariable.Present || got.OriginalImage != "original" {
		t.Fatalf("round trip lost restoration state: %#v", got)
	}
}

func TestValidateCleanupRefusesConcurrentImageChange(t *testing.T) {
	err := ValidateCleanupImage("quay.io/other:changed", "quay.io/example:pr", "original")
	if err == nil {
		t.Fatal("expected concurrent-change error")
	}
}

func TestSanitizeShadowDeployment(t *testing.T) {
	input := []byte(`{"metadata":{"name":"gen-ai-ui","namespace":"redhat-ods-applications","uid":"u","resourceVersion":"1","ownerReferences":[{"name":"dashboard"}],"labels":{"platform.opendatahub.io/part-of":"dashboard","keep":"yes"}},"spec":{"selector":{"matchLabels":{"app":"gen-ai-ui"}},"template":{"metadata":{"labels":{"app":"gen-ai-ui","platform.opendatahub.io/part-of":"dashboard"}},"spec":{"containers":[{"name":"gen-ai-ui","image":"original"}],"serviceAccountName":"preserve-me"}}}}`)
	manifest, err := ShadowManifest(input, "gen-ai-ui-pr-session", "gen-ai-ui", "quay.io/example:pr", "session")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(manifest, &got); err != nil {
		t.Fatal(err)
	}
	metadata := got["metadata"].(map[string]any)
	if metadata["name"] != "gen-ai-ui-pr-session" || metadata["ownerReferences"] != nil {
		t.Fatalf("shadow identity not sanitized: %#v", metadata)
	}
	labels := metadata["labels"].(map[string]any)
	if labels[SessionLabel] != "session" || labels["platform.opendatahub.io/part-of"] != nil {
		t.Fatalf("shadow labels not sanitized: %#v", labels)
	}
	spec := got["spec"].(map[string]any)
	matchLabels := spec["selector"].(map[string]any)["matchLabels"].(map[string]any)
	template := spec["template"].(map[string]any)
	templateLabels := template["metadata"].(map[string]any)["labels"].(map[string]any)
	if matchLabels[SessionLabel] != "session" || templateLabels[SessionLabel] != "session" {
		t.Fatalf("shadow selector could overlap the managed deployment: %#v", spec)
	}
	containers := template["spec"].(map[string]any)["containers"].([]any)
	if containers[0].(map[string]any)["image"] != "quay.io/example:pr" || template["spec"].(map[string]any)["serviceAccountName"] != "preserve-me" {
		t.Fatalf("shadow image or workload fields not preserved: %#v", template)
	}
}

func TestLiveRoutePatchTargetsOnlyTheClonedDashboardService(t *testing.T) {
	patch, err := LiveRoutePatch("rhods-dashboard-pr-session")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(patch, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["path"] != "/spec/rules/0/backendRefs/0/name" || got[0]["value"] != "rhods-dashboard-pr-session" {
		t.Fatalf("unexpected route patch: %#v", got)
	}
}

func TestStackServiceSelectorIncludesWorkloadIdentity(t *testing.T) {
	manifest, err := StackServiceManifest([]byte(`{"metadata":{"name":"source"},"spec":{"selector":{"app":"source"},"ports":[{"port":8143}]}}`), "clone", "session", "gen-ai")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(manifest, &got); err != nil {
		t.Fatal(err)
	}
	selector := got["spec"].(map[string]any)["selector"].(map[string]any)
	if len(selector) != 2 || selector[SessionLabel] != "session" || selector[WorkloadLabel] != "gen-ai" {
		t.Fatalf("selector can overlap a different clone: %#v", selector)
	}
}

func TestSetContainerEnvReplacesOnlyRequestedGatewayDomain(t *testing.T) {
	input := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"rhods-dashboard","env":[{"name":"GATEWAY_DOMAIN","value":"rh-ai.apps.example.com"},{"name":"KEEP","value":"unchanged"}]},{"name":"sidecar","env":[{"name":"GATEWAY_DOMAIN","value":"sidecar.example.com"}]}]}}}}`)
	manifest, err := SetContainerEnv(input, "rhods-dashboard", "GATEWAY_DOMAIN", "test.apps.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(manifest, &got); err != nil {
		t.Fatal(err)
	}
	containers := got["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	dashboardEnv := containers[0].(map[string]any)["env"].([]any)
	if dashboardEnv[0].(map[string]any)["value"] != "test.apps.example.com" || dashboardEnv[1].(map[string]any)["value"] != "unchanged" {
		t.Fatalf("dashboard env was not updated precisely: %#v", dashboardEnv)
	}
	if containers[1].(map[string]any)["env"].([]any)[0].(map[string]any)["value"] != "sidecar.example.com" {
		t.Fatalf("unrelated container was modified: %#v", containers[1])
	}
}

func TestAddPassthroughProviderPreservesExistingInferenceProviders(t *testing.T) {
	input := "providers:\n  inference:\n  - provider_id: endpoint-1\n    provider_type: remote::openai\n  vector_io:\n  - provider_id: pgvector\n"
	got, err := AddPassthroughProvider(input, "https://rh-ai.example.com/gen-ai/api/v1/genai-proxy/ns/project")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "provider_id: endpoint-1") || !strings.Contains(got, "provider_id: genai-bff-proxy") || !strings.Contains(got, "provider_type: remote::passthrough") {
		t.Fatalf("provider was not added without preserving existing entries: %s", got)
	}
	if strings.Index(got, "provider_id: genai-bff-proxy") > strings.Index(got, "  vector_io:") {
		t.Fatalf("provider was added to the wrong YAML section: %s", got)
	}
}
