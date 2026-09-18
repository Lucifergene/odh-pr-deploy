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

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

type Tool struct {
	runner   Runner
	stateDir string
}

func New(runner Runner, stateDir string) *Tool { return &Tool{runner: runner, stateDir: stateDir} }

type DeployOptions struct {
	Context, Namespace, Component, Image, Mode, HostImage string
	PR                                                    int
	AllowManaged                                          bool
	AllowLive                                             bool
}

type Session = core.Session

func (t *Tool) oc(ctx context.Context, contextName, namespace string, args ...string) ([]byte, error) {
	base := []string{"--context", contextName}
	if namespace != "" {
		base = append(base, "-n", namespace)
	}
	return t.runner.Run(ctx, "oc", append(base, args...)...)
}

func (t *Tool) Save(session Session) error {
	if err := os.MkdirAll(t.stateDir, 0700); err != nil {
		return err
	}
	data, err := core.MarshalSession(session)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(t.stateDir, session.ID+".*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(name, t.sessionPath(session.ID))
}

func (t *Tool) Load(id string) (Session, error) {
	data, err := os.ReadFile(t.sessionPath(id))
	if err != nil {
		return Session{}, err
	}
	return core.UnmarshalSession(data)
}

func (t *Tool) Sessions() ([]Session, error) {
	entries, err := os.ReadDir(t.stateDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), "-shadow.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(t.stateDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		session, err := core.UnmarshalSession(data)
		if err != nil {
			return nil, fmt.Errorf("read session %s: %w", entry.Name(), err)
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func (t *Tool) sessionPath(id string) string { return filepath.Join(t.stateDir, id+".json") }

func (t *Tool) Deploy(ctx context.Context, options DeployOptions) (Session, error) {
	if options.Context == "" || options.Namespace == "" || options.Component == "" {
		return Session{}, fmt.Errorf("--context, --namespace, and --component are required")
	}
	if options.Mode == "" {
		options.Mode = "stack"
	}
	if options.Mode != "shadow" && options.Mode != "managed" && options.Mode != "stack" && options.Mode != "live" {
		return Session{}, fmt.Errorf("unsupported mode %q", options.Mode)
	}
	if options.Mode == "managed" && !options.AllowManaged {
		return Session{}, fmt.Errorf("managed updates require --allow-managed-update")
	}
	if options.Mode == "live" && !options.AllowLive {
		return Session{}, fmt.Errorf("live traffic switching requires --allow-live-traffic")
	}
	component, err := core.ComponentFor(options.Component)
	if err != nil {
		return Session{}, err
	}
	image, err := t.resolveImage(ctx, options, component)
	if err != nil {
		return Session{}, err
	}
	image, err = t.verifyAndPinImage(ctx, options.Context, image)
	if err != nil {
		return Session{}, err
	}
	deploymentJSON, err := t.oc(ctx, options.Context, options.Namespace, "get", "deployment", component.Deployment, "-o", "json")
	if err != nil {
		return Session{}, err
	}
	if options.Mode == "managed" && controllerOwned(deploymentJSON) {
		return Session{}, fmt.Errorf("deployment/%s is controller-owned; managed mode would be immediately reconciled. Use --mode shadow", component.Deployment)
	}
	originalImage, err := deploymentImage(deploymentJSON, component.Container)
	if err != nil {
		return Session{}, err
	}
	version, err := t.oc(ctx, options.Context, "", "get", "clusterversion", "version", "-o", "json")
	if err != nil {
		return Session{}, fmt.Errorf("read cluster version: %w", err)
	}
	clusterVersion, err := clusterVersion(version)
	if err != nil {
		return Session{}, err
	}
	dashboard, err := t.oc(ctx, options.Context, options.Namespace, "get", "dashboard", "default-dashboard", "-o", "json")
	if err != nil {
		return Session{}, fmt.Errorf("read Dashboard baseline: %w", err)
	}
	rhoaiVersion, err := dashboardVersion(dashboard)
	if err != nil {
		return Session{}, err
	}
	id := fmt.Sprintf("%s-%s", component.Name, time.Now().UTC().Format("20060102t150405z"))
	session := Session{ID: id, Mode: options.Mode, Context: options.Context, Namespace: options.Namespace, Component: component, Image: image, OriginalImage: originalImage, ImageEnv: component.ImageEnv, ClusterVersion: clusterVersion, RHOAIVersion: rhoaiVersion}

	if options.Mode == "shadow" {
		shadowName := component.Deployment + "-pr-" + time.Now().UTC().Format("150405")
		manifest, err := core.ShadowManifest(deploymentJSON, shadowName, component.Container, image, id)
		if err != nil {
			return Session{}, err
		}
		session.ShadowDeployment = shadowName
		if err := t.Save(session); err != nil {
			return Session{}, err
		}
		manifestPath := filepath.Join(t.stateDir, id+"-shadow.json")
		if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
			return Session{}, err
		}
		if _, err := t.oc(ctx, options.Context, options.Namespace, "apply", "-f", manifestPath); err != nil {
			return Session{}, err
		}
		if err := t.rollout(ctx, session, shadowName); err != nil {
			return Session{}, err
		}
		return session, nil
	}
	if options.Mode == "stack" || options.Mode == "live" {
		return t.deployStack(ctx, options, component, image, deploymentJSON, session, options.Mode == "live")
	}

	if ok, err := t.canPatch(ctx, options.Context, options.Namespace); err != nil || !ok {
		return Session{}, fmt.Errorf("managed update requires patch deployments permission: %w", err)
	}
	if err := t.Save(session); err != nil {
		return Session{}, err
	}
	patch, err := core.DeploymentImagePatch(component.Container, image)
	if err != nil {
		return Session{}, err
	}
	if _, err := t.oc(ctx, options.Context, options.Namespace, "patch", "deployment", component.Deployment, "--type", "strategic", "-p", string(patch)); err != nil {
		return Session{}, err
	}
	if err := t.waitForImage(ctx, session, component.Deployment, image); err != nil {
		return Session{}, err
	}
	if err := t.rollout(ctx, session, component.Deployment); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (t *Tool) Cleanup(ctx context.Context, id string) error {
	session, err := t.Load(id)
	if err != nil {
		return err
	}
	if session.Completed {
		return nil
	}
	if session.Mode == "shadow" {
		current, err := t.oc(ctx, session.Context, session.Namespace, "get", "deployment", session.Component.Deployment, "-o", "json")
		if err != nil {
			return err
		}
		image, err := deploymentImage(current, session.Component.Container)
		if err != nil {
			return err
		}
		if image != session.OriginalImage {
			return fmt.Errorf("refusing shadow cleanup: managed deployment image changed from %q to %q", session.OriginalImage, image)
		}
		if _, err := t.oc(ctx, session.Context, session.Namespace, "delete", "deployment", session.ShadowDeployment, "--ignore-not-found"); err != nil {
			return err
		}
		session.Completed = true
		return t.Save(session)
	}
	if session.Mode == "stack" || session.Mode == "live" {
		if session.OGXConfigPatched != "" {
			configMap, err := t.oc(ctx, session.Context, session.OGXConfigNamespace, "get", "configmap", session.OGXConfigName, "-o", "json")
			if err != nil {
				return err
			}
			current, err := configMapYAML(configMap)
			if err != nil {
				return err
			}
			if current != session.OGXConfigPatched && current != session.OGXConfigOriginal {
				return fmt.Errorf("refusing OGX cleanup: ConfigMap changed outside this session")
			}
			if current == session.OGXConfigPatched {
				if err := t.patchConfigMapYAML(ctx, session.Context, session.OGXConfigNamespace, session.OGXConfigName, session.OGXConfigOriginal); err != nil {
					return err
				}
				if _, err := t.oc(ctx, session.Context, session.OGXConfigNamespace, "delete", "pod", "-l", "ogx.io/server=lsd-genai-playground"); err != nil {
					return err
				}
			}
		}
		if session.Mode == "live" {
			currentRoute, err := t.oc(ctx, session.Context, session.Namespace, "get", "httproute", "rhods-dashboard", "-o", "json")
			if err != nil {
				return err
			}
			currentTarget, err := routeTarget(currentRoute)
			if err != nil {
				return err
			}
			expected := "rhods-dashboard-pr-" + strings.ReplaceAll(strings.TrimPrefix(session.ID, session.Component.Name+"-"), "t", "")
			if currentTarget != session.RouteTarget && currentTarget != expected {
				return fmt.Errorf("refusing live cleanup: gateway route changed outside session to %q", currentTarget)
			}
			if currentTarget == expected {
				patch, err := core.LiveRoutePatch(session.RouteTarget)
				if err != nil {
					return err
				}
				if _, err = t.oc(ctx, session.Context, session.Namespace, "patch", "httproute", "rhods-dashboard", "--type=json", "-p", string(patch)); err != nil {
					return err
				}
			}
			if session.OperatorReplicas != nil {
				if _, err := t.oc(ctx, session.Context, session.Namespace, "scale", "deployment", "dashboard-operator", "--replicas="+fmt.Sprint(*session.OperatorReplicas)); err != nil {
					return err
				}
				if _, err := t.oc(ctx, session.Context, session.Namespace, "rollout", "status", "deployment/dashboard-operator", "--timeout=10m"); err != nil {
					return err
				}
			}
		}
		current, err := t.oc(ctx, session.Context, session.Namespace, "get", "deployment", session.Component.Deployment, "-o", "json")
		if err != nil {
			return err
		}
		image, err := deploymentImage(current, session.Component.Container)
		if err != nil {
			return err
		}
		if image != session.OriginalImage {
			return fmt.Errorf("refusing stack cleanup: managed deployment image changed from %q to %q", session.OriginalImage, image)
		}
		for _, resource := range session.Resources {
			namespace, target := session.Namespace, resource
			if parts := strings.SplitN(resource, "|", 2); len(parts) == 2 {
				namespace, target = parts[0], parts[1]
			}
			parts := strings.SplitN(target, "/", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid recorded resource %q", resource)
			}
			if _, err := t.oc(ctx, session.Context, namespace, "delete", parts[0], parts[1], "--ignore-not-found"); err != nil {
				return err
			}
		}
		session.Completed = true
		return t.Save(session)
	}
	managedJSON, err := t.oc(ctx, session.Context, session.Namespace, "get", "deployment", session.Component.Deployment, "-o", "json")
	if err != nil {
		return err
	}
	currentImage, err := deploymentImage(managedJSON, session.Component.Container)
	if err != nil {
		return err
	}
	if err := core.ValidateCleanupImage(currentImage, session.Image, session.OriginalImage); err != nil {
		return err
	}
	if currentImage == session.OriginalImage {
		session.Completed = true
		return t.Save(session)
	}
	patch, err := core.DeploymentImagePatch(session.Component.Container, session.OriginalImage)
	if err != nil {
		return err
	}
	if _, err := t.oc(ctx, session.Context, session.Namespace, "patch", "deployment", session.Component.Deployment, "--type", "strategic", "-p", string(patch)); err != nil {
		return err
	}
	if err := t.waitForImage(ctx, session, session.Component.Deployment, session.OriginalImage); err != nil {
		return err
	}
	if err := t.rollout(ctx, session, session.Component.Deployment); err != nil {
		return err
	}
	currentDeployment, err := t.oc(ctx, session.Context, session.Namespace, "get", "deployment", session.Component.Deployment, "-o", "json")
	if err != nil {
		return err
	}
	image, err := deploymentImage(currentDeployment, session.Component.Container)
	if err != nil {
		return err
	}
	if image != session.OriginalImage {
		return fmt.Errorf("restore verification failed: got image %q, want %q", image, session.OriginalImage)
	}
	session.Completed = true
	return t.Save(session)
}

// PrepareOGXPassthrough makes an existing project’s OGX configuration usable
// with the Responses API. Its exact previous value is session-persisted before
// the patch, and Cleanup refuses to overwrite a concurrent change.
func (t *Tool) PrepareOGXPassthrough(ctx context.Context, id, project string) error {
	session, err := t.Load(id)
	if err != nil {
		return err
	}
	if session.Mode != "live" {
		return fmt.Errorf("OGX preparation requires a live session")
	}
	if project == "" {
		return fmt.Errorf("--project is required")
	}
	configMap, err := t.oc(ctx, session.Context, project, "get", "configmap", "llama-stack-config", "-o", "json")
	if err != nil {
		return err
	}
	original, err := configMapYAML(configMap)
	if err != nil {
		return err
	}
	baseURL := "https://rh-ai.apps.rosa.akundu-cluster.vbry.p3.openshiftapps.com/gen-ai/api/v1/genai-proxy/ns/" + project
	patched, err := core.AddPassthroughProvider(original, baseURL)
	if err != nil {
		return err
	}
	if session.OGXConfigPatched != "" {
		return fmt.Errorf("session %s already prepared OGX", id)
	}
	session.OGXConfigNamespace, session.OGXConfigName = project, "llama-stack-config"
	session.OGXConfigOriginal, session.OGXConfigPatched = original, patched
	if err := t.Save(session); err != nil {
		return err
	}
	if original == patched {
		return nil
	}
	if err := t.patchConfigMapYAML(ctx, session.Context, project, "llama-stack-config", patched); err != nil {
		return err
	}
	_, err = t.oc(ctx, session.Context, project, "delete", "pod", "-l", "ogx.io/server=lsd-genai-playground")
	return err
}

func (t *Tool) patchConfigMapYAML(ctx context.Context, contextName, namespace, name, yaml string) error {
	patch, err := json.Marshal([]map[string]any{{"op": "replace", "path": "/data/config.yaml", "value": yaml}})
	if err != nil {
		return err
	}
	_, err = t.oc(ctx, contextName, namespace, "patch", "configmap", name, "--type=json", "-p", string(patch))
	return err
}

func configMapYAML(data []byte) (string, error) {
	var value struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	yaml, ok := value.Data["config.yaml"]
	if !ok {
		return "", fmt.Errorf("ConfigMap has no data.config.yaml")
	}
	return yaml, nil
}

// ActivateLive switches the authenticated production gateway only after the
// isolated stack is ready. It is separately callable so interrupted terminals
// cannot leave a half-applied traffic change.
func (t *Tool) ActivateLive(ctx context.Context, id string) error {
	session, err := t.Load(id)
	if err != nil {
		return err
	}
	if session.Mode != "live" {
		return fmt.Errorf("session %s is not a live session", id)
	}
	suffix := strings.ReplaceAll(strings.TrimPrefix(session.ID, session.Component.Name+"-"), "t", "")
	dashboardName := "rhods-dashboard-pr-" + suffix
	if err := t.rollout(ctx, session, dashboardName); err != nil {
		return err
	}
	operator, err := t.oc(ctx, session.Context, session.Namespace, "get", "deployment", "dashboard-operator", "-o", "json")
	if err != nil {
		return err
	}
	replicas, err := deploymentReplicas(operator)
	if err != nil {
		return err
	}
	liveRoute, err := t.oc(ctx, session.Context, session.Namespace, "get", "httproute", "rhods-dashboard", "-o", "json")
	if err != nil {
		return err
	}
	target, err := routeTarget(liveRoute)
	if err != nil {
		return err
	}
	if session.RouteTarget != "" {
		return fmt.Errorf("session %s is already activated", id)
	}
	session.OperatorReplicas, session.RouteTarget = &replicas, target
	if err := t.Save(session); err != nil {
		return err
	}
	if _, err := t.oc(ctx, session.Context, session.Namespace, "scale", "deployment", "dashboard-operator", "--replicas=0"); err != nil {
		return err
	}
	patch, err := core.LiveRoutePatch(dashboardName)
	if err != nil {
		return err
	}
	if _, err = t.oc(ctx, session.Context, session.Namespace, "patch", "httproute", "rhods-dashboard", "--type=json", "-p", string(patch)); err != nil {
		return err
	}
	session.URL = "https://rh-ai.apps.rosa.akundu-cluster.vbry.p3.openshiftapps.com/gen-ai-studio/playground"
	return t.Save(session)
}

func (t *Tool) rollout(ctx context.Context, session Session, deployment string) error {
	_, err := t.oc(ctx, session.Context, session.Namespace, "rollout", "status", "deployment/"+deployment, "--timeout=10m")
	return err
}

func (t *Tool) waitForImage(ctx context.Context, session Session, deployment, expected string) error {
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		data, err := t.oc(ctx, session.Context, session.Namespace, "get", "deployment", deployment, "-o", "json")
		if err != nil {
			return err
		}
		image, err := deploymentImage(data, session.Component.Container)
		if err != nil {
			return err
		}
		if image == expected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for deployment/%s image %q (current %q)", deployment, expected, image)
		case <-ticker.C:
		}
	}
}

func (t *Tool) canPatch(ctx context.Context, contextName, namespace string) (bool, error) {
	output, err := t.oc(ctx, contextName, namespace, "auth", "can-i", "patch", "deployments.apps")
	return strings.TrimSpace(string(output)) == "yes", err
}

func (t *Tool) resolveImage(ctx context.Context, options DeployOptions, component core.Component) (string, error) {
	if options.Image != "" {
		return options.Image, nil
	}
	if options.PR <= 0 {
		return "", fmt.Errorf("provide --image or --pr")
	}
	output, err := t.runner.Run(ctx, "gh", "pr", "view", fmt.Sprint(options.PR), "--repo", "opendatahub-io/odh-dashboard", "--json", "headRefOid", "--jq", ".headRefOid")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(output))
	if len(sha) < 7 {
		return "", fmt.Errorf("could not resolve PR %d head commit", options.PR)
	}
	if component.Name == "gen-ai" {
		return "quay.io/opendatahub/odh-mod-arch-gen-ai:odh-pr-" + sha, nil
	}
	return "", fmt.Errorf("PR image resolution is not configured for %s; use --image", component.Name)
}

func (t *Tool) deployStack(ctx context.Context, options DeployOptions, component core.Component, image string, componentDeployment []byte, session Session, live bool) (Session, error) {
	hostImage := options.HostImage
	if hostImage == "" && options.PR > 0 {
		output, err := t.runner.Run(ctx, "gh", "pr", "view", fmt.Sprint(options.PR), "--repo", "opendatahub-io/odh-dashboard", "--json", "headRefOid", "--jq", ".headRefOid")
		if err != nil {
			return Session{}, err
		}
		hostImage = "quay.io/opendatahub/odh-dashboard:odh-pr-" + strings.TrimSpace(string(output))
	}
	if hostImage == "" {
		return Session{}, fmt.Errorf("stack deployment requires --host-image or --pr")
	}
	hostImage, err := t.verifyAndPinImage(ctx, options.Context, hostImage)
	if err != nil {
		return Session{}, err
	}
	componentService, err := t.oc(ctx, options.Context, options.Namespace, "get", "service", component.Service, "-o", "json")
	if err != nil {
		return Session{}, err
	}
	hostDeployment, err := t.oc(ctx, options.Context, options.Namespace, "get", "deployment", "rhods-dashboard", "-o", "json")
	if err != nil {
		return Session{}, err
	}
	hostService, err := t.oc(ctx, options.Context, options.Namespace, "get", "service", "rhods-dashboard", "-o", "json")
	if err != nil {
		return Session{}, err
	}
	federation, err := t.oc(ctx, options.Context, options.Namespace, "get", "configmap", "federation-config", "-o", "json")
	if err != nil {
		return Session{}, err
	}
	route, err := t.oc(ctx, options.Context, options.Namespace, "get", "route", "rhods-dashboard", "-o", "json")
	if err != nil {
		return Session{}, err
	}
	hostname, err := stackHostname(route, session.ID)
	if err != nil {
		return Session{}, err
	}
	suffix := strings.ReplaceAll(strings.TrimPrefix(session.ID, component.Name+"-"), "t", "")
	componentName := component.Deployment + "-pr-" + suffix
	componentServiceName := component.Service + "-pr-" + suffix
	dashboardName := "rhods-dashboard-pr-" + suffix
	federationName := "federation-config-pr-" + suffix
	routeName := "odh-pr-" + suffix
	componentManifest, err := core.ShadowManifest(componentDeployment, componentName, component.Container, image, session.ID)
	if err != nil {
		return Session{}, err
	}
	componentManifest, err = core.AddWorkloadLabel(componentManifest, "component")
	if err != nil {
		return Session{}, err
	}
	componentServiceManifest, err := core.StackServiceManifest(componentService, componentServiceName, session.ID, "component")
	if err != nil {
		return Session{}, err
	}
	hostManifest, err := core.ShadowManifest(hostDeployment, dashboardName, "rhods-dashboard", hostImage, session.ID)
	if err != nil {
		return Session{}, err
	}
	if !live {
		hostManifest, err = core.SetContainerEnv(hostManifest, "rhods-dashboard", "GATEWAY_DOMAIN", hostname)
		if err != nil {
			return Session{}, err
		}
	}
	var hostObject map[string]any
	if err := json.Unmarshal(hostManifest, &hostObject); err != nil {
		return Session{}, err
	}
	containers := hostObject["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	for _, item := range containers {
		c := item.(map[string]any)
		if c["name"] == "rhods-dashboard" {
			for _, envItem := range c["env"].([]any) {
				env := envItem.(map[string]any)
				if env["name"] == "MODULE_FEDERATION_CONFIG" {
					env["valueFrom"].(map[string]any)["configMapKeyRef"].(map[string]any)["name"] = federationName
				}
			}
		}
	}
	hostManifest, err = json.Marshal(hostObject)
	if err != nil {
		return Session{}, err
	}
	hostManifest, err = core.AddWorkloadLabel(hostManifest, "dashboard")
	if err != nil {
		return Session{}, err
	}
	hostServiceManifest, err := core.StackServiceManifest(hostService, dashboardName, session.ID, "dashboard")
	if err != nil {
		return Session{}, err
	}
	federationManifest, err := core.StackFederationManifest(federation, federationName, session.ID, componentServiceName, dashboardName)
	if err != nil {
		return Session{}, err
	}
	routeManifest, err := core.StackRouteManifest(routeName, options.Namespace, session.ID, hostname, dashboardName)
	if err != nil {
		return Session{}, err
	}
	ingressRouteManifest, err := core.StackIngressRouteManifest(routeName, session.ID, hostname)
	if err != nil {
		return Session{}, err
	}
	resources := []struct {
		name      string
		data      []byte
		namespace string
	}{{componentName, componentManifest, options.Namespace}, {componentServiceName, componentServiceManifest, options.Namespace}, {federationName, federationManifest, options.Namespace}, {dashboardName, hostManifest, options.Namespace}, {dashboardName + "-service", hostServiceManifest, options.Namespace}, {routeName, routeManifest, options.Namespace}, {routeName + "-ingress", ingressRouteManifest, "openshift-ingress"}}
	session.HostImage, session.URL = hostImage, "https://"+hostname+"/gen-ai-studio/playground"
	session.Resources = []string{"deployment/" + componentName, "service/" + componentServiceName, "configmap/" + federationName, "deployment/" + dashboardName, "service/" + dashboardName, "httproute/" + routeName, "openshift-ingress|route/" + routeName}
	if err := t.Save(session); err != nil {
		return Session{}, err
	}
	for _, resource := range resources {
		path := filepath.Join(t.stateDir, session.ID+"-"+resource.name+".json")
		if err := os.WriteFile(path, resource.data, 0600); err != nil {
			return Session{}, err
		}
		if _, err := t.oc(ctx, options.Context, resource.namespace, "apply", "-f", path); err != nil {
			return Session{}, err
		}
	}
	if err := t.rollout(ctx, session, componentName); err != nil {
		return Session{}, err
	}
	if err := t.rollout(ctx, session, dashboardName); err != nil {
		return Session{}, err
	}
	if live {
		operator, err := t.oc(ctx, options.Context, options.Namespace, "get", "deployment", "dashboard-operator", "-o", "json")
		if err != nil {
			return Session{}, err
		}
		replicas, err := deploymentReplicas(operator)
		if err != nil {
			return Session{}, err
		}
		liveRoute, err := t.oc(ctx, options.Context, options.Namespace, "get", "httproute", "rhods-dashboard", "-o", "json")
		if err != nil {
			return Session{}, err
		}
		target, err := routeTarget(liveRoute)
		if err != nil {
			return Session{}, err
		}
		session.OperatorReplicas, session.RouteTarget = &replicas, target
		if err := t.Save(session); err != nil {
			return Session{}, err
		}
		if _, err := t.oc(ctx, options.Context, options.Namespace, "scale", "deployment", "dashboard-operator", "--replicas=0"); err != nil {
			return Session{}, err
		}
		patch, err := core.LiveRoutePatch(dashboardName)
		if err != nil {
			return Session{}, err
		}
		if _, err = t.oc(ctx, options.Context, options.Namespace, "patch", "httproute", "rhods-dashboard", "--type=json", "-p", string(patch)); err != nil {
			return Session{}, err
		}
		session.URL = "https://rh-ai.apps.rosa.akundu-cluster.vbry.p3.openshiftapps.com/gen-ai-studio/playground"
		if err := t.Save(session); err != nil {
			return Session{}, err
		}
	}
	return session, nil
}

func deploymentReplicas(data []byte) (int32, error) {
	var value struct {
		Spec struct {
			Replicas *int32 `json:"replicas"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	if value.Spec.Replicas == nil {
		return 1, nil
	}
	return *value.Spec.Replicas, nil
}

func routeTarget(data []byte) (string, error) {
	var value struct {
		Spec struct {
			Rules []struct {
				BackendRefs []struct {
					Name string `json:"name"`
				} `json:"backendRefs"`
			} `json:"rules"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return "", err
	}
	if len(value.Spec.Rules) == 0 || len(value.Spec.Rules[0].BackendRefs) == 0 || value.Spec.Rules[0].BackendRefs[0].Name == "" {
		return "", fmt.Errorf("gateway route has no first backend")
	}
	return value.Spec.Rules[0].BackendRefs[0].Name, nil
}

func stackHostname(route []byte, session string) (string, error) {
	var value struct {
		Spec struct {
			Host string `json:"host"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(route, &value); err != nil {
		return "", err
	}
	parts := strings.SplitN(value.Spec.Host, ".apps.", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("could not derive apps domain from route host %q", value.Spec.Host)
	}
	return "odh-pr-" + strings.ReplaceAll(session, "_", "-") + ".apps." + parts[1], nil
}

func (t *Tool) verifyAndPinImage(ctx context.Context, contextName, image string) (string, error) {
	output, err := t.oc(ctx, contextName, "", "image", "info", "--filter-by-os=linux/amd64", image, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("verify image %q: %w", image, err)
	}
	var information struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(output, &information); err != nil {
		return "", fmt.Errorf("parse image information: %w", err)
	}
	return core.PinImage(image, information.Digest)
}

func deploymentImage(data []byte, container string) (string, error) {
	var document struct {
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
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	for _, value := range document.Spec.Template.Spec.Containers {
		if value.Name == container {
			return value.Image, nil
		}
	}
	return "", fmt.Errorf("container %q not found", container)
}

func imageVariable(data []byte, variable string) (string, core.ImageVariable, error) {
	var document struct {
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
	if err := json.Unmarshal(data, &document); err != nil {
		return "", core.ImageVariable{}, err
	}
	for _, container := range document.Spec.Template.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == variable {
				return container.Name, core.ImageVariable{Present: true, Value: env.Value}, nil
			}
		}
	}
	if len(document.Spec.Template.Spec.Containers) == 0 {
		return "", core.ImageVariable{}, fmt.Errorf("operator deployment has no containers")
	}
	return document.Spec.Template.Spec.Containers[0].Name, core.ImageVariable{}, nil
}

func clusterVersion(data []byte) (string, error) {
	var document struct {
		Status struct {
			Desired struct {
				Version string `json:"version"`
			} `json:"desired"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	if document.Status.Desired.Version == "" {
		return "", fmt.Errorf("cluster version is empty")
	}
	return document.Status.Desired.Version, nil
}

func dashboardVersion(data []byte) (string, error) {
	var document struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return "", err
	}
	version := document.Metadata.Annotations["platform.opendatahub.io/version"]
	if version == "" {
		return "", fmt.Errorf("Dashboard RHOAI version annotation is empty")
	}
	return version, nil
}

func controllerOwned(data []byte) bool {
	var document struct {
		Metadata struct {
			OwnerReferences []struct {
				Controller bool `json:"controller"`
			} `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &document) != nil {
		return false
	}
	for _, owner := range document.Metadata.OwnerReferences {
		if owner.Controller {
			return true
		}
	}
	return false
}
