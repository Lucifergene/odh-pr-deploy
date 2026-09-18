package app

import "testing"

func TestCSVImageVariableFindsExactRelatedImage(t *testing.T) {
	csv := []byte(`{"spec":{"install":{"spec":{"deployments":[{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"UNRELATED","value":"keep"},{"name":"RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE","value":"original"}]}]}}}}]}}}}`)
	index, value, err := csvImageVariable(csv, "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE")
	if err != nil {
		t.Fatalf("csvImageVariable() error = %v", err)
	}
	if index != 1 || value != "original" {
		t.Fatalf("csvImageVariable() = (%d, %q), want (1, original)", index, value)
	}
}

func TestCSVImageVariableRejectsMissingVariable(t *testing.T) {
	csv := []byte(`{"spec":{"install":{"spec":{"deployments":[{"spec":{"template":{"spec":{"containers":[{"env":[]}]}}}}]}}}}`)
	if _, _, err := csvImageVariable(csv, "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE"); err == nil {
		t.Fatal("csvImageVariable() error = nil, want missing-variable error")
	}
}
