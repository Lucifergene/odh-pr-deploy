package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/akundu/odh-pr-deploy/internal/core"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	out, err := c.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Tool struct {
	runner   Runner
	stateDir string
}

func New(r Runner, stateDir string) *Tool { return &Tool{runner: r, stateDir: stateDir} }

type DeployOptions struct {
	Context, Namespace, Component, Image, ManifestsDir string
	PR                                                 int
}
type Session = core.Session

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
	f, e := os.CreateTemp(t.stateDir, ".session-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Chmod(0600); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Close(); e != nil {
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
	var ss []Session
	for _, x := range es {
		if !x.IsDir() && strings.HasSuffix(x.Name(), ".json") {
			s, e := t.Load(strings.TrimSuffix(x.Name(), ".json"))
			if e != nil {
				return nil, e
			}
			ss = append(ss, s)
		}
	}
	return ss, nil
}

// Deploy changes the RHOAI operator CSV's related-image input. Direct edits to
// the controller-owned Dashboard operator are reconciled away.
func (t *Tool) Deploy(ctx context.Context, o DeployOptions) (Session, error) {
	if o.Context == "" || o.Component == "" {
		return Session{}, fmt.Errorf("--context and --component are required")
	}
	c, e := core.ComponentFor(o.Component)
	if e != nil {
		return Session{}, e
	}
	ns := o.Namespace
	if ns == "" {
		ns, e = t.dashboardNamespace(ctx, o.Context)
		if e != nil {
			return Session{}, e
		}
	}
	image, e := t.image(ctx, o, c)
	if e != nil {
		return Session{}, e
	}
	image, e = t.pin(ctx, o.Context, image)
	if e != nil {
		return Session{}, e
	}
	b, e := t.oc(ctx, o.Context, ns, "get", "deployment", c.Deployment, "-o", "json")
	if e != nil {
		return Session{}, e
	}
	original, e := deploymentImage(b, c.Container)
	if e != nil {
		return Session{}, e
	}
	csvName, e := t.rhoaiCSV(ctx, o.Context)
	if e != nil {
		return Session{}, e
	}
	b, e = t.oc(ctx, o.Context, "redhat-ods-operator", "get", "csv", csvName, "-o", "json")
	if e != nil {
		return Session{}, e
	}
	_, rv, e := csvDeployment(b)
	if e != nil {
		return Session{}, e
	}
	index, previous, e := csvImageVariable(b, c.ImageEnv)
	if e != nil {
		return Session{}, e
	}
	id, e := core.NewSessionID(c.Name)
	if e != nil {
		return Session{}, e
	}
	annotation := "odh-pr-deploy.openshift.io/reconcile-" + id
	s := Session{ID: id, Context: o.Context, Namespace: ns, Component: c, Image: image, OriginalImage: original, CSVNamespace: "redhat-ods-operator", CSVName: csvName, CSVResourceVersion: rv, CSVEnvIndex: index, OriginalVariable: core.ImageVariable{Present: true, Value: previous}, DSCAnnotation: annotation}
	if e = t.Save(s); e != nil {
		return Session{}, e
	}
	if e = t.patchCSVImage(ctx, s, previous, image, rv); e != nil {
		return Session{}, e
	}
	if e = t.waitOperator(ctx, s); e != nil {
		return Session{}, e
	}
	if e = t.triggerDashboardReconcile(ctx, s); e != nil {
		return Session{}, e
	}
	if e = t.waitImage(ctx, s, image); e != nil {
		return Session{}, e
	}
	return s, nil
}

func (t *Tool) createPVC(ctx context.Context, s Session) error {
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": s.PVCName, "namespace": s.CSVNamespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "odh-pr-deploy", "odh-pr-deploy/session": s.ID}},
		"spec":     map[string]any{"accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]string{"storage": "1Gi"}}},
	})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(t.stateDir, ".pvc-*.json")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(manifest); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	_, err = t.oc(ctx, s.Context, s.CSVNamespace, "apply", "-f", path)
	return err
}
func (t *Tool) Cleanup(ctx context.Context, id string) error {
	s, e := t.Load(id)
	if e != nil {
		return e
	}
	if s.Completed {
		return nil
	}
	if s.CSVName == "" {
		s.Completed = true
		return t.Save(s)
	}
	b, e := t.oc(ctx, s.Context, s.CSVNamespace, "get", "csv", s.CSVName, "-o", "json")
	if e != nil {
		return e
	}
	_, rv, e := csvDeployment(b)
	if e != nil {
		return e
	}
	_, current, e := csvImageVariable(b, s.Component.ImageEnv)
	if e != nil {
		return e
	}
	if current == s.OriginalVariable.Value {
		s.Completed = true
		return t.Save(s)
	}
	if current != s.Image {
		return fmt.Errorf("refusing cleanup: CSV image was changed outside this session")
	}
	if e = t.patchCSVImage(ctx, s, current, s.OriginalVariable.Value, rv); e != nil {
		return e
	}
	if e = t.waitOperator(ctx, s); e != nil {
		return e
	}
	if e = t.triggerDashboardReconcile(ctx, s); e != nil {
		return e
	}
	if e = t.waitImage(ctx, s, s.OriginalImage); e != nil {
		return e
	}
	if _, e = t.oc(ctx, s.Context, "", "annotate", "datasciencecluster", "default-dsc", s.DSCAnnotation+"-"); e != nil {
		return e
	}
	s.Completed = true
	return t.Save(s)
}

func (t *Tool) restoreLiveOperator(ctx context.Context, s Session) error {
	b, err := t.oc(ctx, s.Context, s.CSVNamespace, "get", "deployment", "rhods-operator", "-o", "json")
	if err != nil {
		return err
	}
	if !operatorHasSessionMount(b, s.PVCName) {
		return fmt.Errorf("refusing cleanup: live operator no longer has this session PVC mount")
	}
	var current struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err = json.Unmarshal(b, &current); err != nil {
		return err
	}
	var original struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err = json.Unmarshal(s.OperatorOriginalSpec, &original); err != nil {
		return fmt.Errorf("decode saved operator spec: %w", err)
	}
	template := original.Spec["template"]
	var templateValue map[string]any
	if err = json.Unmarshal(template, &templateValue); err != nil {
		return err
	}
	podSpec := templateValue["spec"].(map[string]any)
	patch, err := json.Marshal([]any{
		map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": current.Metadata.ResourceVersion},
		map[string]any{"op": "replace", "path": "/spec/replicas", "value": original.Spec["replicas"]},
		map[string]any{"op": "replace", "path": "/spec/strategy", "value": original.Spec["strategy"]},
		map[string]any{"op": "replace", "path": "/spec/template/spec/securityContext", "value": podSpec["securityContext"]},
		map[string]any{"op": "replace", "path": "/spec/template/spec/volumes", "value": podSpec["volumes"]},
		map[string]any{"op": "replace", "path": "/spec/template/spec/containers/0/volumeMounts", "value": podSpec["containers"].([]any)[0].(map[string]any)["volumeMounts"]},
	})
	if err != nil {
		return err
	}
	_, err = t.oc(ctx, s.Context, s.CSVNamespace, "patch", "deployment", "rhods-operator", "--type=json", "-p", string(patch))
	return err
}
func (t *Tool) replaceCSVDeployment(ctx context.Context, s Session, wanted json.RawMessage, rv string) error {
	p, e := json.Marshal([]any{map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": rv}, map[string]any{"op": "replace", "path": "/spec/install/spec/deployments/0", "value": json.RawMessage(wanted)}})
	if e != nil {
		return e
	}
	_, e = t.oc(ctx, s.Context, s.CSVNamespace, "patch", "csv", s.CSVName, "--type=json", "-p", string(p))
	return e
}

func (t *Tool) patchCSVImage(ctx context.Context, s Session, expected, image, rv string) error {
	path := fmt.Sprintf("/spec/install/spec/deployments/0/spec/template/spec/containers/0/env/%d/value", s.CSVEnvIndex)
	patch, err := json.Marshal([]any{
		map[string]any{"op": "test", "path": "/metadata/resourceVersion", "value": rv},
		map[string]any{"op": "test", "path": path, "value": expected},
		map[string]any{"op": "replace", "path": path, "value": image},
	})
	if err != nil {
		return err
	}
	_, err = t.oc(ctx, s.Context, s.CSVNamespace, "patch", "csv", s.CSVName, "--type=json", "-p", string(patch))
	return err
}

func (t *Tool) triggerDashboardReconcile(ctx context.Context, s Session) error {
	_, err := t.oc(ctx, s.Context, "", "annotate", "datasciencecluster", "default-dsc", s.DSCAnnotation+"="+s.ID, "--overwrite")
	return err
}
func (t *Tool) waitOperator(ctx context.Context, s Session) error {
	_, e := t.oc(ctx, s.Context, s.CSVNamespace, "rollout", "status", "deployment/rhods-operator", "--timeout=10m")
	return e
}
func (t *Tool) restartOperator(ctx context.Context, s Session) error {
	if _, e := t.oc(ctx, s.Context, s.CSVNamespace, "rollout", "restart", "deployment/rhods-operator"); e != nil {
		return e
	}
	return t.waitOperator(ctx, s)
}
func (t *Tool) copyManifests(ctx context.Context, s Session) error {
	p, e := t.oc(ctx, s.Context, s.CSVNamespace, "get", "pod", "-l", "name=rhods-operator", "-o", "jsonpath={.items[0].metadata.name}")
	if e != nil {
		return e
	}
	pod := strings.TrimSpace(string(p))
	if _, e = t.oc(ctx, s.Context, s.CSVNamespace, "exec", pod, "--", "sh", "-c", "mkdir -p /opt/manifests/dashboard"); e != nil {
		return e
	}
	_, e = t.oc(ctx, s.Context, "", "cp", filepath.Join(s.ManifestsDir, "manifests")+"/.", s.CSVNamespace+"/"+pod+":/opt/manifests/dashboard")
	if e != nil {
		return e
	}
	// The copied params file deliberately contains a development default. Pin
	// this session's resolved PR digest after copy so the nested Dashboard
	// operator renders the exact image that was preflighted.
	_, e = t.oc(ctx, s.Context, s.CSVNamespace, "exec", pod, "--", "sed", "-i", "s|^gen-ai-ui-image=.*|gen-ai-ui-image="+s.Image+"|", "/opt/manifests/dashboard/modules/gen-ai/params.env")
	return e
}
func (t *Tool) waitImage(ctx context.Context, s Session, want string) error {
	until := time.Now().Add(12 * time.Minute)
	for time.Now().Before(until) {
		b, e := t.oc(ctx, s.Context, s.Namespace, "get", "deployment", s.Component.Deployment, "-o", "json")
		if e != nil {
			return e
		}
		got, e := deploymentImage(b, s.Component.Container)
		if e != nil {
			return e
		}
		if got == want {
			_, e = t.oc(ctx, s.Context, s.Namespace, "rollout", "status", "deployment/"+s.Component.Deployment, "--timeout=10m")
			return e
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s image %s", s.Component.Deployment, want)
}
func (t *Tool) dashboardNamespace(ctx context.Context, k string) (string, error) {
	b, e := t.oc(ctx, k, "", "get", "deployment", "-A", "-l", "app.kubernetes.io/name=gen-ai", "-o", "json")
	if e != nil {
		return "", e
	}
	var v struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if e = json.Unmarshal(b, &v); e != nil || len(v.Items) != 1 {
		return "", fmt.Errorf("could not uniquely discover dashboard namespace")
	}
	return v.Items[0].Metadata.Namespace, nil
}
func (t *Tool) rhoaiCSV(ctx context.Context, k string) (string, error) {
	b, e := t.oc(ctx, k, "redhat-ods-operator", "get", "subscription", "rhods-operator", "-o", "jsonpath={.status.installedCSV}")
	if e != nil {
		return "", e
	}
	if n := strings.TrimSpace(string(b)); n != "" {
		return n, nil
	}
	return "", fmt.Errorf("RHOAI operator subscription has no installed CSV")
}

func (t *Tool) allowedFSGroup(ctx context.Context, k, namespace string) (int64, error) {
	b, err := t.oc(ctx, k, "", "get", "namespace", namespace, "-o", "json")
	if err != nil {
		return 0, err
	}
	var ns struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err = json.Unmarshal(b, &ns); err != nil {
		return 0, err
	}
	rangeValue := ns.Metadata.Annotations["openshift.io/sa.scc.supplemental-groups"]
	first := strings.Split(rangeValue, "/")[0]
	value, err := strconv.ParseInt(first, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("cannot derive an allowed fsGroup from namespace %q", namespace)
	}
	return value, nil
}
func (t *Tool) image(ctx context.Context, o DeployOptions, c core.Component) (string, error) {
	if o.Image != "" {
		return o.Image, nil
	}
	if o.PR <= 0 {
		return "", fmt.Errorf("--image or --pr is required")
	}
	b, e := t.runner.Run(ctx, "gh", "api", fmt.Sprintf("repos/opendatahub-io/odh-dashboard/pulls/%d", o.PR), "--jq", ".head.sha")
	if e != nil {
		return "", e
	}
	return "quay.io/opendatahub/odh-mod-arch-gen-ai:odh-pr-" + strings.TrimSpace(string(b)), nil
}
func (t *Tool) pin(ctx context.Context, k, image string) (string, error) {
	b, e := t.oc(ctx, k, "", "image", "info", image, "-o", "json")
	if e != nil {
		return "", e
	}
	var info struct {
		Digest string `json:"digest"`
	}
	if e = json.Unmarshal(b, &info); e != nil {
		return "", fmt.Errorf("decode image metadata: %w", e)
	}
	return core.PinImage(image, info.Digest)
}
func deploymentImage(b []byte, container string) (string, error) {
	var d struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if e := json.Unmarshal(b, &d); e != nil {
		return "", e
	}
	for _, x := range d.Spec.Template.Spec.Containers {
		if x.Name == container {
			return x.Image, nil
		}
	}
	return "", fmt.Errorf("container %q not found", container)
}
func csvDeployment(b []byte) (json.RawMessage, string, error) {
	var v struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Install struct {
				Spec struct {
					Deployments []json.RawMessage `json:"deployments"`
				} `json:"spec"`
			} `json:"install"`
		} `json:"spec"`
	}
	if e := json.Unmarshal(b, &v); e != nil {
		return nil, "", e
	}
	if len(v.Spec.Install.Spec.Deployments) != 1 {
		return nil, "", fmt.Errorf("expected exactly one operator deployment in CSV")
	}
	return v.Spec.Install.Spec.Deployments[0], v.Metadata.ResourceVersion, nil
}

func csvImageVariable(b []byte, name string) (int, string, error) {
	var v struct {
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
	if err := json.Unmarshal(b, &v); err != nil {
		return 0, "", err
	}
	if len(v.Spec.Install.Spec.Deployments) != 1 || len(v.Spec.Install.Spec.Deployments[0].Spec.Template.Spec.Containers) != 1 {
		return 0, "", fmt.Errorf("unexpected RHOAI CSV deployment layout")
	}
	for i, env := range v.Spec.Install.Spec.Deployments[0].Spec.Template.Spec.Containers[0].Env {
		if env.Name == name {
			return i, env.Value, nil
		}
	}
	return 0, "", fmt.Errorf("CSV does not define %s", name)
}
func componentDevSpec(original json.RawMessage, pvc string, fsGroup int64) (json.RawMessage, error) {
	var d map[string]any
	if e := json.Unmarshal(original, &d); e != nil {
		return nil, e
	}
	spec, ok := d["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CSV deployment lacks spec")
	}
	tmpl, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CSV deployment lacks pod template")
	}
	pod, ok := tmpl["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("CSV deployment lacks pod spec")
	}
	cs, ok := pod["containers"].([]any)
	if !ok || len(cs) != 1 {
		return nil, fmt.Errorf("expected exactly one operator container")
	}
	first, ok := cs[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid operator container")
	}
	vols, _ := pod["volumes"].([]any)
	mounts, _ := first["volumeMounts"].([]any)
	spec["replicas"] = float64(1)
	spec["strategy"] = map[string]any{"type": "Recreate"}
	securityContext, _ := pod["securityContext"].(map[string]any)
	if securityContext == nil {
		securityContext = map[string]any{}
	}
	securityContext["fsGroup"] = float64(fsGroup)
	pod["securityContext"] = securityContext
	pod["volumes"] = append(vols, map[string]any{"name": "odh-pr-deploy-manifests", "persistentVolumeClaim": map[string]any{"claimName": pvc}})
	first["volumeMounts"] = append(mounts, map[string]any{"name": "odh-pr-deploy-manifests", "mountPath": "/opt/manifests/dashboard"})
	return json.Marshal(d)
}

func operatorSpec(b []byte) json.RawMessage {
	var v struct {
		Spec json.RawMessage `json:"spec"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	return v.Spec
}
func operatorHasSessionMount(b []byte, pvc string) bool {
	var v struct {
		Spec struct {
			Template struct {
				Spec struct {
					Volumes []struct {
						Name                  string `json:"name"`
						PersistentVolumeClaim *struct {
							ClaimName string `json:"claimName"`
						} `json:"persistentVolumeClaim"`
					} `json:"volumes"`
					Containers []struct {
						VolumeMounts []struct{ Name, MountPath string } `json:"volumeMounts"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if json.Unmarshal(b, &v) != nil {
		return false
	}
	volume := false
	for _, x := range v.Spec.Template.Spec.Volumes {
		if x.Name == "odh-pr-deploy-manifests" && x.PersistentVolumeClaim != nil && x.PersistentVolumeClaim.ClaimName == pvc {
			volume = true
		}
	}
	if !volume {
		return false
	}
	for _, c := range v.Spec.Template.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.Name == "odh-pr-deploy-manifests" && m.MountPath == "/opt/manifests/dashboard" {
				return true
			}
		}
	}
	return false
}
func validateManifests(dir string) error {
	_, e := os.Stat(filepath.Join(dir, "manifests", "modules", "gen-ai", "params.env"))
	if e != nil {
		return fmt.Errorf("--manifests-dir must be an odh-dashboard checkout: %w", e)
	}
	return nil
}
