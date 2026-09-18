package core

import (
	"encoding/json"
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
