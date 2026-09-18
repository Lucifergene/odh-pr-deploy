package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akundu/odh-pr-deploy/internal/core"
)

const leaseName = "odh-pr-deploy-rhoai-images"

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	b, e := c.CombinedOutput()
	if e != nil {
		return b, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), e, strings.TrimSpace(string(b)))
	}
	return b, nil
}

type Tool struct {
	runner   Runner
	stateDir string
}

func New(r Runner, stateDir string) *Tool { return &Tool{runner: r, stateDir: stateDir} }

type DeployOptions struct {
	Context, Namespace, Image, DashboardImage string
	PR                                        int
}
type Session = core.StackSession
type csvImageValue struct {
	Index int
	Value string
}

func (t *Tool) oc(ctx context.Context, k, n string, a ...string) ([]byte, error) {
	args := []string{"--context", k}
	if n != "" {
		args = append(args, "-n", n)
	}
	return t.runner.Run(ctx, "oc", append(args, a...)...)
}
func (t *Tool) path(id string) (string, error) {
	if !core.ValidSessionID(id) {
		return "", fmt.Errorf("invalid session ID")
	}
	return filepath.Join(t.stateDir, id+".json"), nil
}
func (t *Tool) Save(s Session) error {
	p, e := t.path(s.ID)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(t.stateDir, 0700); e != nil {
		return e
	}
	b, e := core.MarshalSession(s)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(t.stateDir, ".session-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Chmod(0600)
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), p)
}
func (t *Tool) Load(id string) (Session, error) {
	p, e := t.path(id)
	if e != nil {
		return Session{}, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return Session{}, e
	}
	return core.UnmarshalSession(b)
}
func (t *Tool) Sessions() ([]Session, error) {
	es, e := os.ReadDir(t.stateDir)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	out := []Session{}
	for _, x := range es {
		if !x.IsDir() && strings.HasSuffix(x.Name(), ".json") {
			s, e := t.Load(strings.TrimSuffix(x.Name(), ".json"))
			if e != nil {
				return nil, e
			}
			out = append(out, s)
		}
	}
	return out, nil
}

func (t *Tool) Inspect(ctx context.Context, k, n string) (Session, error) {
	return t.discover(ctx, k, n, core.GenAIStackFromSHA(""))
}
func (t *Tool) Deploy(ctx context.Context, o DeployOptions) (Session, error) {
	if o.Context == "" {
		return Session{}, fmt.Errorf("--context is required")
	}
	stack, e := t.resolveStack(ctx, o)
	if e != nil {
		return Session{}, e
	}
	s, e := t.discover(ctx, o.Context, o.Namespace, stack)
	if e != nil {
		return Session{}, e
	}
	if e = t.Save(s); e != nil {
		return Session{}, e
	}
	if e = t.acquire(ctx, s); e != nil {
		return s, e
	}
	defer t.release(context.Background(), s)
	if e = t.patch(ctx, s, false); e != nil {
		return s, e
	}
	if e = t.reconcile(ctx, s, "deploy"); e != nil {
		return s, e
	}
	if e = t.waitAll(ctx, s, false); e != nil {
		return s, e
	}
	return s, nil
}
func (t *Tool) Cleanup(ctx context.Context, id string) error {
	s, e := t.Load(id)
	if e != nil {
		return e
	}
	if s.Completed {
		return nil
	}
	if e = t.acquire(ctx, s); e != nil {
		return e
	}
	defer t.release(context.Background(), s)
	b, e := t.oc(ctx, s.Context, s.CSVNamespace, "get", "csv", s.CSVName, "-o", "json")
	if e != nil {
		return e
	}
	values, _, e := csvImageVariables(b)
	if e != nil {
		return e
	}
	original := true
	desired := true
	for _, x := range s.Overrides {
		v, ok := values[x.Component.ImageEnv]
		if !ok {
			return fmt.Errorf("CSV no longer defines %s", x.Component.ImageEnv)
		}
		original = original && v.Value == x.OriginalCSVValue
		desired = desired && v.Value == x.Image
	}
	if original {
		s.Completed = true
		return t.Save(s)
	}
	if !desired {
		return fmt.Errorf("refusing cleanup: one or more CSV images changed outside this session")
	}
	if e = t.patch(ctx, s, true); e != nil {
		return e
	}
	if e = t.reconcile(ctx, s, "cleanup"); e != nil {
		return e
	}
	if e = t.waitAll(ctx, s, true); e != nil {
		return e
	}
	if _, e = t.oc(ctx, s.Context, "", "annotate", "datasciencecluster", "default-dsc", s.Annotation+"-"); e != nil {
		return e
	}
	s.Completed = true
	return t.Save(s)
}
func (t *Tool) resolveStack(ctx context.Context, o DeployOptions) ([]core.Component, error) {
	if o.PR > 0 {
		if o.Image != "" || o.DashboardImage != "" {
			return nil, fmt.Errorf("use either --pr or the explicit image pair")
		}
		b, e := t.runner.Run(ctx, "gh", "api", fmt.Sprintf("repos/opendatahub-io/odh-dashboard/pulls/%d", o.PR), "--jq", ".head.sha")
		if e != nil {
			return nil, e
		}
		return core.GenAIStackFromSHA(strings.TrimSpace(string(b))), nil
	}
	return core.CustomGenAIStack(o.Image, o.DashboardImage)
}
func (t *Tool) discover(ctx context.Context, k, n string, stack []core.Component) (Session, error) {
	if k == "" {
		return Session{}, fmt.Errorf("--context is required")
	}
	csvNS, csvName, e := t.rhoaiCSV(ctx, k)
	if e != nil {
		return Session{}, e
	}
	b, e := t.oc(ctx, k, csvNS, "get", "csv", csvName, "-o", "json")
	if e != nil {
		return Session{}, e
	}
	values, _, e := csvImageVariables(b)
	if e != nil {
		return Session{}, e
	}
	if n == "" {
		n, e = t.dashboardNamespace(ctx, k)
		if e != nil {
			return Session{}, e
		}
	}
	id, e := core.NewSessionID()
	if e != nil {
		return Session{}, e
	}
	s := Session{ID: id, Context: k, Namespace: n, CSVNamespace: csvNS, CSVName: csvName, Annotation: "odh-pr-deploy.openshift.io/reconcile-" + id, Phase: "inspected"}
	for _, c := range stack {
		v, ok := values[c.ImageEnv]
		if !ok {
			return Session{}, fmt.Errorf("CSV does not define %s", c.ImageEnv)
		}
		d, e := t.oc(ctx, k, n, "get", "deployment", c.Deployment, "-o", "json")
		if e != nil {
			return Session{}, e
		}
		old, e := deploymentImage(d, c.Container)
		if e != nil {
			return Session{}, e
		}
		image := c.Image
		if image != "" {
			image, e = t.pin(ctx, k, image)
			if e != nil {
				return Session{}, e
			}
		}
		c.Image = image
		s.Overrides = append(s.Overrides, core.ImageOverride{Component: c, Image: image, OriginalCSVValue: v.Value, OriginalWorkloadImage: old, CSVEnvIndex: v.Index})
	}
	return s, nil
}
func (t *Tool) rhoaiCSV(ctx context.Context, k string) (string, string, error) {
	b, e := t.oc(ctx, k, "", "get", "subscription", "-A", "-o", "json")
	if e != nil {
		return "", "", e
	}
	var v struct {
		Items []struct {
			Metadata struct{ Namespace, Name string }
			Status   struct {
				InstalledCSV string `json:"installedCSV"`
			}
		}
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return "", "", e
	}
	matches := v.Items[:0]
	for _, x := range v.Items {
		if x.Metadata.Name == "rhods-operator" && x.Status.InstalledCSV != "" {
			matches = append(matches, x)
		}
	}
	if len(matches) != 1 {
		return "", "", fmt.Errorf("expected exactly one installed rhods-operator Subscription")
	}
	return matches[0].Metadata.Namespace, matches[0].Status.InstalledCSV, nil
}
func (t *Tool) dashboardNamespace(ctx context.Context, k string) (string, error) {
	b, e := t.oc(ctx, k, "", "get", "deployment", "-A", "-o", "json")
	if e != nil {
		return "", e
	}
	var v struct {
		Items []struct {
			Metadata struct{ Namespace, Name string }
		}
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return "", e
	}
	ns := ""
	for _, x := range v.Items {
		if x.Metadata.Name == "gen-ai-ui" {
			if ns != "" && ns != x.Metadata.Namespace {
				return "", fmt.Errorf("multiple gen-ai-ui deployments found")
			}
			ns = x.Metadata.Namespace
		}
	}
	if ns == "" {
		return "", fmt.Errorf("gen-ai-ui deployment not found")
	}
	return ns, nil
}
func csvImageVariables(b []byte) (map[string]csvImageValue, string, error) {
	var v struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		}
		Spec struct {
			Install struct {
				Spec struct {
					Deployments []struct {
						Spec struct {
							Template struct {
								Spec struct {
									Containers []struct {
										Env []struct{ Name, Value string } `json:"env"`
									} `json:"containers"`
								} `json:"spec"`
							} `json:"template"`
						} `json:"spec"`
					} `json:"deployments"`
				} `json:"spec"`
			} `json:"install"`
		} `json:"spec"`
	}
	if e := json.Unmarshal(b, &v); e != nil {
		return nil, "", e
	}
	if len(v.Spec.Install.Spec.Deployments) != 1 || len(v.Spec.Install.Spec.Deployments[0].Spec.Template.Spec.Containers) != 1 {
		return nil, "", fmt.Errorf("unexpected RHOAI CSV deployment layout")
	}
	out := map[string]csvImageValue{}
	for i, x := range v.Spec.Install.Spec.Deployments[0].Spec.Template.Spec.Containers[0].Env {
		if x.Name == "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE" || x.Name == "RELATED_IMAGE_ODH_DASHBOARD_IMAGE" {
			out[x.Name] = csvImageValue{i, x.Value}
		}
	}
	if len(out) != 2 {
		return nil, "", fmt.Errorf("CSV lacks required GenAI stack image variables")
	}
	return out, v.Metadata.ResourceVersion, nil
}
func pairedCSVPatch(rv string, values map[string]csvImageValue, wanted map[string]string) ([]byte, error) {
	ops := []any{map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": rv}}
	for _, name := range []string{"RELATED_IMAGE_ODH_DASHBOARD_IMAGE", "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE"} {
		v, ok := values[name]
		if !ok || wanted[name] == "" {
			return nil, fmt.Errorf("missing %s", name)
		}
		path := fmt.Sprintf("/spec/install/spec/deployments/0/spec/template/spec/containers/0/env/%d/value", v.Index)
		ops = append(ops, map[string]any{"op": "test", "path": path, "value": v.Value}, map[string]any{"op": "replace", "path": path, "value": wanted[name]})
	}
	return json.Marshal(ops)
}
func (t *Tool) patch(ctx context.Context, s Session, restore bool) error {
	b, e := t.oc(ctx, s.Context, s.CSVNamespace, "get", "csv", s.CSVName, "-o", "json")
	if e != nil {
		return e
	}
	values, rv, e := csvImageVariables(b)
	if e != nil {
		return e
	}
	wanted := map[string]string{}
	for _, x := range s.Overrides {
		if restore {
			wanted[x.Component.ImageEnv] = x.OriginalCSVValue
		} else {
			wanted[x.Component.ImageEnv] = x.Image
		}
		expected := x.OriginalCSVValue
		if restore {
			expected = x.Image
		}
		if values[x.Component.ImageEnv].Value != expected {
			return fmt.Errorf("refusing patch: %s changed outside session", x.Component.Name)
		}
	}
	p, e := pairedCSVPatch(rv, values, wanted)
	if e != nil {
		return e
	}
	_, e = t.oc(ctx, s.Context, s.CSVNamespace, "patch", "csv", s.CSVName, "--type=json", "-p", string(p))
	return e
}
func (t *Tool) reconcile(ctx context.Context, s Session, phase string) error {
	s.Phase = phase
	if e := t.Save(s); e != nil {
		return e
	}
	if _, e := t.oc(ctx, s.Context, "", "annotate", "datasciencecluster", "default-dsc", s.Annotation+"="+phase+"-"+fmt.Sprint(time.Now().UnixNano()), "--overwrite"); e != nil {
		return e
	}
	_, e := t.oc(ctx, s.Context, s.CSVNamespace, "rollout", "status", "deployment/rhods-operator", "--timeout=10m")
	return e
}
func (t *Tool) waitAll(ctx context.Context, s Session, restore bool) error {
	deadline := time.Now().Add(12 * time.Minute)
	for time.Now().Before(deadline) {
		ok := true
		for _, x := range s.Overrides {
			b, e := t.oc(ctx, s.Context, s.Namespace, "get", "deployment", x.Component.Deployment, "-o", "json")
			if e != nil {
				return e
			}
			got, e := deploymentImage(b, x.Component.Container)
			if e != nil {
				return e
			}
			want := x.Image
			if restore {
				want = x.OriginalWorkloadImage
			}
			if got != want {
				ok = false
				break
			}
		}
		if ok {
			for _, x := range s.Overrides {
				if _, e := t.oc(ctx, s.Context, s.Namespace, "rollout", "status", "deployment/"+x.Component.Deployment, "--timeout=10m"); e != nil {
					return e
				}
			}
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for paired stack rollout")
}
func (t *Tool) pin(ctx context.Context, k, image string) (string, error) {
	b, e := t.oc(ctx, k, "", "image", "info", image, "-o", "json")
	if e != nil {
		return "", e
	}
	var v struct{ Digest string }
	if e = json.Unmarshal(b, &v); e != nil {
		return "", e
	}
	return core.PinImage(image, v.Digest)
}
func deploymentImage(b []byte, container string) (string, error) {
	var v struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct{ Name, Image string } `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if e := json.Unmarshal(b, &v); e != nil {
		return "", e
	}
	for _, c := range v.Spec.Template.Spec.Containers {
		if c.Name == container {
			return c.Image, nil
		}
	}
	return "", fmt.Errorf("container %q not found", container)
}
func (t *Tool) acquire(ctx context.Context, s Session) error {
	manifest, e := json.Marshal(map[string]any{
		"apiVersion": "coordination.k8s.io/v1",
		"kind":       "Lease",
		"metadata": map[string]string{
			"name":      leaseName,
			"namespace": s.CSVNamespace,
		},
	})
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(t.stateDir, ".lease-*.json")
	if e != nil {
		return e
	}
	path := f.Name()
	defer os.Remove(path)
	if _, e = f.Write(manifest); e == nil {
		e = f.Close()
	} else {
		_ = f.Close()
	}
	if e != nil {
		return e
	}
	_, e = t.oc(ctx, s.Context, s.CSVNamespace, "create", "-f", path)
	if e != nil {
		return fmt.Errorf("acquire deployment lease: %w", e)
	}
	return nil
}
func (t *Tool) release(ctx context.Context, s Session) {
	_, _ = t.oc(ctx, s.Context, s.CSVNamespace, "delete", "lease", leaseName, "--ignore-not-found")
}
