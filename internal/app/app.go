package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/akundu/odh-pr-deploy/internal/core"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, n string, a ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, n, a...)
	o, e := c.CombinedOutput()
	if e != nil {
		return o, fmt.Errorf("%s %s: %w: %s", n, strings.Join(a, " "), e, strings.TrimSpace(string(o)))
	}
	return o, nil
}

type Tool struct {
	runner   Runner
	stateDir string
}

func New(r Runner, d string) *Tool { return &Tool{r, d} }

type DeployOptions struct {
	Context, Namespace, Component, Image string
	PR                                   int
}
type Session = core.Session

func (t *Tool) oc(ctx context.Context, c, n string, a ...string) ([]byte, error) {
	x := []string{"--context", c}
	if n != "" {
		x = append(x, "-n", n)
	}
	return t.runner.Run(ctx, "oc", append(x, a...)...)
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
		f.Close()
		return e
	}
	if e = f.Chmod(0600); e != nil {
		f.Close()
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
	var r []Session
	for _, x := range es {
		if x.IsDir() || !strings.HasSuffix(x.Name(), ".json") {
			continue
		}
		s, e := t.Load(strings.TrimSuffix(x.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		r = append(r, s)
	}
	return r, nil
}

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
	opNS, opName, e := t.operator(ctx, o.Context, ns)
	if e != nil {
		return Session{}, e
	}
	img, e := t.image(ctx, o, c)
	if e != nil {
		return Session{}, e
	}
	img, e = t.pin(ctx, o.Context, img)
	if e != nil {
		return Session{}, e
	}
	operator, e := t.oc(ctx, o.Context, opNS, "get", "deployment", opName, "-o", "json")
	if e != nil {
		return Session{}, e
	}
	uid, rv, old, e := operatorState(operator, c.ImageEnv)
	if e != nil {
		return Session{}, e
	}
	dep, e := t.oc(ctx, o.Context, ns, "get", "deployment", c.Deployment, "-o", "json")
	if e != nil {
		return Session{}, e
	}
	original, e := deploymentImage(dep, c.Container)
	if e != nil {
		return Session{}, e
	}
	id, e := core.NewSessionID(c.Name)
	if e != nil {
		return Session{}, e
	}
	s := Session{ID: id, Context: o.Context, Namespace: ns, Component: c, Image: img, OriginalImage: original, OperatorNamespace: opNS, OperatorDeployment: opName, OperatorContainer: "manager", OperatorUID: uid, OperatorResourceVersion: rv, OriginalVariable: old}
	if s.DashboardURL, e = t.dashboardURL(ctx, o.Context, ns); e != nil {
		return Session{}, e
	}
	if e = t.Save(s); e != nil {
		return Session{}, e
	}
	p, e := core.UpdatePatch(s.OperatorContainer, c.ImageEnv, img, rv)
	if e != nil {
		return Session{}, e
	}
	if _, e = t.oc(ctx, o.Context, opNS, "patch", "deployment", opName, "--type=strategic", "-p", string(p)); e != nil {
		return Session{}, e
	}
	if e = t.rollout(ctx, s, opNS, opName); e != nil {
		return Session{}, e
	}
	if e = t.waitImage(ctx, s, c.Deployment, img); e != nil {
		return Session{}, e
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
	b, e := t.oc(ctx, s.Context, s.OperatorNamespace, "get", "deployment", s.OperatorDeployment, "-o", "json")
	if e != nil {
		return e
	}
	uid, rv, cur, e := operatorState(b, s.Component.ImageEnv)
	if e != nil {
		return e
	}
	if uid != s.OperatorUID {
		return fmt.Errorf("refusing cleanup: operator was replaced")
	}
	if cur.Present == s.OriginalVariable.Present && cur.Value == s.OriginalVariable.Value {
		s.Completed = true
		return t.Save(s)
	}
	if cur.Present != true || cur.Value != s.Image {
		return fmt.Errorf("refusing cleanup: override changed outside session")
	}
	p, e := core.RestorePatch(s.OperatorContainer, s.Component.ImageEnv, s.OriginalVariable, rv)
	if e != nil {
		return e
	}
	if _, e = t.oc(ctx, s.Context, s.OperatorNamespace, "patch", "deployment", s.OperatorDeployment, "--type=strategic", "-p", string(p)); e != nil {
		return e
	}
	if e = t.rollout(ctx, s, s.OperatorNamespace, s.OperatorDeployment); e != nil {
		return e
	}
	if e = t.waitImage(ctx, s, s.Component.Deployment, s.OriginalImage); e != nil {
		return e
	}
	s.Completed = true
	return t.Save(s)
}
func (t *Tool) rollout(ctx context.Context, s Session, n, d string) error {
	_, e := t.oc(ctx, s.Context, n, "rollout", "status", "deployment/"+d, "--timeout=10m")
	return e
}
func (t *Tool) waitImage(ctx context.Context, s Session, d, want string) error {
	end := time.Now().Add(10 * time.Minute)
	for time.Now().Before(end) {
		b, e := t.oc(ctx, s.Context, s.Namespace, "get", "deployment", d, "-o", "json")
		if e != nil {
			return e
		}
		got, e := deploymentImage(b, s.Component.Container)
		if e != nil {
			return e
		}
		if got == want {
			return t.rollout(ctx, s, s.Namespace, d)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for deployment/%s image %q", d, want)
}
func (t *Tool) image(ctx context.Context, o DeployOptions, c core.Component) (string, error) {
	if o.Image != "" {
		return o.Image, nil
	}
	if o.PR <= 0 {
		return "", fmt.Errorf("provide --image or --pr")
	}
	b, e := t.runner.Run(ctx, "gh", "pr", "view", fmt.Sprint(o.PR), "--repo", "opendatahub-io/odh-dashboard", "--json", "headRefOid", "--jq", ".headRefOid")
	if e != nil {
		return "", e
	}
	repo := "odh-mod-arch-gen-ai"
	if c.Name == "dashboard" {
		repo = "odh-dashboard"
	}
	return "quay.io/opendatahub/" + repo + ":odh-pr-" + strings.TrimSpace(string(b)), nil
}
func (t *Tool) pin(ctx context.Context, c, img string) (string, error) {
	b, e := t.oc(ctx, c, "", "image", "info", "--filter-by-os=linux/amd64", img, "-o", "json")
	if e != nil {
		return "", e
	}
	var x struct {
		Digest string `json:"digest"`
	}
	if e = json.Unmarshal(b, &x); e != nil {
		return "", e
	}
	return core.PinImage(img, x.Digest)
}
func (t *Tool) dashboardNamespace(ctx context.Context, c string) (string, error) {
	b, e := t.oc(ctx, c, "", "get", "deployment", "-A", "-o", "json")
	if e != nil {
		return "", e
	}
	var x struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if e = json.Unmarshal(b, &x); e != nil {
		return "", fmt.Errorf("discover Dashboard operator: %w", e)
	}
	var namespace string
	for _, item := range x.Items {
		if item.Metadata.Name != "dashboard-operator" {
			continue
		}
		if namespace != "" || item.Metadata.Namespace == "" {
			return "", fmt.Errorf("cannot uniquely discover Dashboard operator namespace")
		}
		namespace = item.Metadata.Namespace
	}
	if namespace == "" {
		return "", fmt.Errorf("cannot discover Dashboard operator namespace; provide --namespace")
	}
	return namespace, nil
}
func (t *Tool) operator(ctx context.Context, c, ns string) (string, string, error) {
	for _, v := range [][2]string{{ns, "dashboard-operator"}} {
		if _, e := t.oc(ctx, c, v[0], "get", "deployment", v[1], "-o", "name"); e == nil {
			return v[0], v[1], nil
		}
	}
	return "", "", fmt.Errorf("could not discover Dashboard operator")
}
func (t *Tool) dashboardURL(ctx context.Context, c, ns string) (string, error) {
	b, e := t.oc(ctx, c, ns, "get", "route", "rhods-dashboard", "-o", "json")
	if e != nil {
		return "", e
	}
	var x struct {
		Spec struct {
			Host string `json:"host"`
		} `json:"spec"`
	}
	if e = json.Unmarshal(b, &x); e != nil || x.Spec.Host == "" {
		return "", fmt.Errorf("cannot discover Dashboard route")
	}
	return "https://" + x.Spec.Host, nil
}
func operatorState(b []byte, name string) (string, string, core.ImageVariable, error) {
	var x struct {
		Metadata struct {
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if e := json.Unmarshal(b, &x); e != nil {
		return "", "", core.ImageVariable{}, e
	}
	for _, c := range x.Spec.Template.Spec.Containers {
		for _, v := range c.Env {
			if v.Name == name {
				return x.Metadata.UID, x.Metadata.ResourceVersion, core.ImageVariable{Present: true, Value: v.Value}, nil
			}
		}
	}
	return x.Metadata.UID, x.Metadata.ResourceVersion, core.ImageVariable{}, nil
}
func deploymentImage(b []byte, n string) (string, error) {
	var x struct {
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
	if e := json.Unmarshal(b, &x); e != nil {
		return "", e
	}
	for _, c := range x.Spec.Template.Spec.Containers {
		if c.Name == n {
			return c.Image, nil
		}
	}
	return "", fmt.Errorf("container %q not found", n)
}
