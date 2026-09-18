package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type Component struct {
	Name       string `json:"name"`
	Deployment string `json:"deployment"`
	Container  string `json:"container"`
	ImageEnv   string `json:"imageEnv"`
}

var components = map[string]Component{
	"gen-ai":    {Name: "gen-ai", Deployment: "gen-ai-ui", Container: "gen-ai-ui", ImageEnv: "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE"},
	"dashboard": {Name: "dashboard", Deployment: "rhods-dashboard", Container: "rhods-dashboard", ImageEnv: "RELATED_IMAGE_ODH_DASHBOARD_IMAGE"},
}

func ComponentFor(name string) (Component, error) {
	c, ok := components[name]
	if !ok {
		return Component{}, fmt.Errorf("unsupported component %q (supported: gen-ai, dashboard)", name)
	}
	return c, nil
}

type ImageVariable struct {
	Present bool   `json:"present"`
	Value   string `json:"value"`
}
type Session struct {
	ID                      string          `json:"id"`
	Context                 string          `json:"context"`
	Namespace               string          `json:"namespace"`
	Component               Component       `json:"component"`
	Image                   string          `json:"image"`
	OriginalImage           string          `json:"originalImage"`
	OperatorNamespace       string          `json:"operatorNamespace"`
	OperatorDeployment      string          `json:"operatorDeployment"`
	OperatorContainer       string          `json:"operatorContainer"`
	OperatorUID             string          `json:"operatorUID"`
	OperatorResourceVersion string          `json:"operatorResourceVersion"`
	OriginalVariable        ImageVariable   `json:"originalVariable"`
	DashboardURL            string          `json:"dashboardURL"`
	CSVNamespace            string          `json:"csvNamespace"`
	CSVName                 string          `json:"csvName"`
	CSVOriginalDeployment   json.RawMessage `json:"csvOriginalDeployment"`
	CSVPatchedDeployment    json.RawMessage `json:"csvPatchedDeployment"`
	CSVResourceVersion      string          `json:"csvResourceVersion"`
	CSVEnvIndex             int             `json:"csvEnvIndex"`
	DSCAnnotation           string          `json:"dscAnnotation"`
	PVCName                 string          `json:"pvcName"`
	ManifestsDir            string          `json:"manifestsDir"`
	OperatorOriginalSpec    json.RawMessage `json:"operatorOriginalSpec"`
	Completed               bool            `json:"completed"`
}

func NewSessionID(component string) (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return component + "-" + hex.EncodeToString(b), nil
}
func ValidSessionID(id string) bool {
	if len(id) < 9 || strings.ContainsAny(id, "/\\") {
		return false
	}
	for _, r := range id {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
func PinImage(ref, digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("registry did not return a sha256 digest")
	}
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref[:at+1] + digest, nil
	}
	if c := strings.LastIndex(ref, ":"); c > strings.LastIndex(ref, "/") {
		ref = ref[:c]
	}
	return ref + "@" + digest, nil
}
func UpdatePatch(container, name, value, resourceVersion string) ([]byte, error) {
	if container == "" || name == "" || value == "" {
		return nil, fmt.Errorf("container, environment variable, and image are required")
	}
	return deploymentEnvPatch(container, []any{map[string]string{"name": name, "value": value}}, resourceVersion)
}
func RestorePatch(container, name string, original ImageVariable, resourceVersion string) ([]byte, error) {
	if container == "" || name == "" {
		return nil, fmt.Errorf("container and environment variable are required")
	}
	env := map[string]string{"name": name}
	if original.Present {
		env["value"] = original.Value
	} else {
		env["$patch"] = "delete"
	}
	return deploymentEnvPatch(container, []any{env}, resourceVersion)
}
func deploymentEnvPatch(container string, env []any, resourceVersion string) ([]byte, error) {
	containerPatch := map[string]any{"name": container, "env": env}
	podSpec := map[string]any{"containers": []any{containerPatch}}
	template := map[string]any{"spec": podSpec}
	patch := map[string]any{"spec": map[string]any{"template": template}}
	if resourceVersion != "" {
		patch["metadata"] = map[string]string{"resourceVersion": resourceVersion}
	}
	return json.Marshal(patch)
}
func MarshalSession(s Session) ([]byte, error) { return json.MarshalIndent(s, "", "  ") }
func UnmarshalSession(b []byte) (Session, error) {
	var s Session
	err := json.Unmarshal(b, &s)
	return s, err
}

// CanonicalJSON permits semantic comparison of persisted Kubernetes objects.
func CanonicalJSON(raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}
