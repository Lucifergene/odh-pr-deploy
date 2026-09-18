package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCSVImageVariablesFindsBothRequiredInputs(t *testing.T) {
	csv := []byte(`{"metadata":{"resourceVersion":"7"},"spec":{"install":{"spec":{"deployments":[{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"RELATED_IMAGE_ODH_DASHBOARD_IMAGE","value":"dashboard-old"},{"name":"RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE","value":"genai-old"}]}]}}}}]}}}}`)
	values, rv, err := csvImageVariables(csv)
	if err != nil {
		t.Fatal(err)
	}
	if rv != "7" || values["RELATED_IMAGE_ODH_DASHBOARD_IMAGE"].Value != "dashboard-old" || values["RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE"].Value != "genai-old" {
		t.Fatalf("unexpected discovery: %#v, %q", values, rv)
	}
}

func TestPairedCSVPatchGuardsAndReplacesBothImages(t *testing.T) {
	values := map[string]csvImageValue{"RELATED_IMAGE_ODH_DASHBOARD_IMAGE": {Index: 0, Value: "dashboard-old"}, "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE": {Index: 1, Value: "genai-old"}}
	patch, err := pairedCSVPatch("7", values, map[string]string{"RELATED_IMAGE_ODH_DASHBOARD_IMAGE": "dashboard-new", "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE": "genai-new"})
	if err != nil {
		t.Fatal(err)
	}
	var operations []map[string]any
	if err := json.Unmarshal(patch, &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 5 {
		t.Fatalf("got %d operations, want 5", len(operations))
	}
	if operations[0]["op"] != "test" || operations[0]["path"] != "/metadata/resourceVersion" {
		t.Fatalf("missing resource version guard: %#v", operations[0])
	}
	if operations[2]["value"] != "dashboard-new" || operations[4]["value"] != "genai-new" {
		t.Fatalf("did not replace both images: %#v", operations)
	}
}

type deployRunner struct {
	calls   []string
	patched bool
}

func (r *deployRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if name == "gh" {
		return []byte("abc123\n"), nil
	}
	if strings.Contains(call, "get subscription -A") {
		return []byte(`{"items":[{"metadata":{"namespace":"operators","name":"rhods-operator"},"status":{"installedCSV":"rhods.v1"}}]}`), nil
	}
	if strings.Contains(call, "get csv rhods.v1") {
		return []byte(`{"metadata":{"resourceVersion":"7"},"spec":{"install":{"spec":{"deployments":[{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"RELATED_IMAGE_ODH_DASHBOARD_IMAGE","value":"dashboard-old"},{"name":"RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE","value":"genai-old"}]}]}}}}]}}}}`), nil
	}
	if strings.Contains(call, "patch csv rhods.v1") {
		r.patched = true
		return nil, nil
	}
	if strings.Contains(call, "image info") {
		return []byte(`{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), nil
	}
	if strings.Contains(call, "get deployment -A") {
		return []byte(`{"items":[{"metadata":{"namespace":"apps","name":"gen-ai-ui"}}]}`), nil
	}
	if strings.Contains(call, "get deployment gen-ai-ui") {
		image := "genai-old"
		if r.patched {
			image = "quay.io/opendatahub/odh-mod-arch-gen-ai@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		return []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"gen-ai-ui","image":"` + image + `"}]}}}}`), nil
	}
	if strings.Contains(call, "get deployment rhods-dashboard") {
		image := "dashboard-old"
		if r.patched {
			image = "quay.io/opendatahub/odh-dashboard@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}
		return []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"rhods-dashboard","image":"` + image + `"}]}}}}`), nil
	}
	return nil, nil
}
func TestDeployChangesBothImagesInOneGuardedCSVTransaction(t *testing.T) {
	r := &deployRunner{}
	tool := New(r, t.TempDir())
	s, err := tool.Deploy(context.Background(), DeployOptions{Context: "ctx", PR: 9816})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Overrides) != 2 || !r.patched {
		t.Fatalf("deployment did not persist paired stack: %#v", s)
	}
	joined := strings.Join(r.calls, "\n")
	if !strings.Contains(joined, "odh-dashboard@sha256:") || !strings.Contains(joined, "odh-mod-arch-gen-ai@sha256:") {
		t.Fatalf("CSV patch omitted a stack image: %s", joined)
	}
}
