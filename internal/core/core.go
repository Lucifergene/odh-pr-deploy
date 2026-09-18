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
	Image      string `json:"image"`
}

func GenAIStackFromSHA(sha string) []Component {
	genAIImage, dashboardImage := "", ""
	if sha != "" {
		genAIImage = "quay.io/opendatahub/odh-mod-arch-gen-ai:odh-pr-" + sha
		dashboardImage = "quay.io/opendatahub/odh-dashboard:odh-pr-" + sha
	}
	return []Component{
		{Name: "gen-ai", Deployment: "gen-ai-ui", Container: "gen-ai-ui", ImageEnv: "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE", Image: genAIImage},
		{Name: "dashboard", Deployment: "rhods-dashboard", Container: "rhods-dashboard", ImageEnv: "RELATED_IMAGE_ODH_DASHBOARD_IMAGE", Image: dashboardImage},
	}
}
func CustomGenAIStack(genAI, dashboard string) ([]Component, error) {
	if genAI == "" || dashboard == "" {
		return nil, fmt.Errorf("--image and --dashboard-image must be provided together")
	}
	s := GenAIStackFromSHA("")
	s[0].Image, s[1].Image = genAI, dashboard
	return s, nil
}

type ImageOverride struct {
	Component             Component `json:"component"`
	Image                 string    `json:"image"`
	OriginalCSVValue      string    `json:"originalCSVValue"`
	OriginalWorkloadImage string    `json:"originalWorkloadImage"`
	CSVEnvIndex           int       `json:"csvEnvIndex"`
}
type StackSession struct {
	ID           string          `json:"id"`
	Context      string          `json:"context"`
	Namespace    string          `json:"namespace"`
	CSVNamespace string          `json:"csvNamespace"`
	CSVName      string          `json:"csvName"`
	Annotation   string          `json:"annotation"`
	Phase        string          `json:"phase"`
	Overrides    []ImageOverride `json:"overrides"`
	Completed    bool            `json:"completed"`
}

func NewSessionID() (string, error) {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return "genai-stack-" + hex.EncodeToString(b), nil
}
func ValidSessionID(id string) bool {
	if len(id) < 16 || strings.ContainsAny(id, "/\\") {
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
func MarshalSession(s StackSession) ([]byte, error) { return json.MarshalIndent(s, "", "  ") }
func UnmarshalSession(b []byte) (StackSession, error) {
	var s StackSession
	e := json.Unmarshal(b, &s)
	return s, e
}
