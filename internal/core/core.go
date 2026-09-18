package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

const SessionLabel = "odh-pr-deploy.opendatahub.io/session"
const WorkloadLabel = "odh-pr-deploy.opendatahub.io/workload"

// Component describes the operator-owned dashboard workload whose image may be tested.
type Component struct {
	Name       string `json:"name"`
	Deployment string `json:"deployment"`
	Container  string `json:"container"`
	ImageEnv   string `json:"imageEnv"`
	Service    string `json:"service"`
}

var components = map[string]Component{
	"gen-ai": {
		Name:       "gen-ai",
		Deployment: "gen-ai-ui",
		Container:  "gen-ai-ui",
		ImageEnv:   "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE",
		Service:    "odh-dashboard-gen-ai-ui",
	},
}

func ComponentFor(name string) (Component, error) {
	component, ok := components[name]
	if !ok {
		return Component{}, fmt.Errorf("unsupported component %q (supported: gen-ai)", name)
	}
	return component, nil
}

// PinImage turns a tag reference into the immutable digest reference returned by the registry.
func PinImage(reference, digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("registry did not return a sha256 digest")
	}
	if at := strings.Index(reference, "@"); at >= 0 {
		return reference[:at+1] + digest, nil
	}
	lastSlash := strings.LastIndex(reference, "/")
	if colon := strings.LastIndex(reference, ":"); colon > lastSlash {
		reference = reference[:colon]
	}
	return reference + "@" + digest, nil
}

// ImageVariable preserves the important distinction between an absent variable and an empty value.
type ImageVariable struct {
	Present bool   `json:"present"`
	Value   string `json:"value"`
}

// Session is the durable inverse-operation record for a test deployment.
type Session struct {
	ID                 string        `json:"id"`
	Mode               string        `json:"mode"`
	Context            string        `json:"context"`
	Namespace          string        `json:"namespace"`
	Component          Component     `json:"component"`
	Image              string        `json:"image"`
	OriginalImage      string        `json:"originalImage"`
	OperatorNamespace  string        `json:"operatorNamespace"`
	OperatorDeployment string        `json:"operatorDeployment"`
	OperatorContainer  string        `json:"operatorContainer"`
	OriginalVariable   ImageVariable `json:"originalVariable"`
	ImageEnv           string        `json:"imageEnv"`
	ClusterVersion     string        `json:"clusterVersion"`
	RHOAIVersion       string        `json:"rhoaiVersion"`
	ShadowDeployment   string        `json:"shadowDeployment,omitempty"`
	Resources          []string      `json:"resources,omitempty"`
	URL                string        `json:"url,omitempty"`
	HostImage          string        `json:"hostImage,omitempty"`
	OperatorReplicas   *int32        `json:"operatorReplicas,omitempty"`
	RouteTarget        string        `json:"routeTarget,omitempty"`
	OGXConfigNamespace string        `json:"ogxConfigNamespace,omitempty"`
	OGXConfigName      string        `json:"ogxConfigName,omitempty"`
	OGXConfigOriginal  string        `json:"ogxConfigOriginal,omitempty"`
	OGXConfigPatched   string        `json:"ogxConfigPatched,omitempty"`
	Completed          bool          `json:"completed"`
}

// AddPassthroughProvider adds the Responses API provider without disturbing
// existing providers or registered models. The caller retains both documents
// so cleanup can restore the exact original ConfigMap value.
func AddPassthroughProvider(config, baseURL string) (string, error) {
	if baseURL == "" {
		return "", fmt.Errorf("passthrough base URL is required")
	}
	if strings.Contains(config, "provider_id: genai-bff-proxy") {
		return config, nil
	}
	marker := "\n  vector_io:"
	at := strings.Index(config, marker)
	if at < 0 {
		return "", fmt.Errorf("Llama Stack config has no inference provider boundary")
	}
	provider := "\n  - provider_id: genai-bff-proxy\n    provider_type: remote::passthrough\n    config:\n      base_url: " + baseURL + "\n      api_key: \"\"\n      forward_headers:\n        maas_subscription: X-MaaS-Subscription\n        inference_model_source_type: X-Inference-Model-Source-Type\n"
	return config[:at] + provider + config[at:], nil
}

// LiveRoutePatch changes exactly one field on the existing gateway route.
func LiveRoutePatch(service string) ([]byte, error) {
	if service == "" {
		return nil, fmt.Errorf("route service is required")
	}
	return json.Marshal([]map[string]any{{"op": "replace", "path": "/spec/rules/0/backendRefs/0/name", "value": service}})
}

// StackManifest returns a controller-independent copy of a Service. Its selector is
// narrowed with the session label so it cannot select production pods.
func StackServiceManifest(data []byte, name, session, workload string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("parse service: %w", err)
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("service metadata is required")
	}
	metadata["name"] = name
	for _, key := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "ownerReferences", "annotations"} {
		delete(metadata, key)
	}
	ensureMap(metadata, "labels")[SessionLabel] = session
	delete(object, "status")
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("service spec is required")
	}
	for _, key := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy", "healthCheckNodePort"} {
		delete(spec, key)
	}
	// Replace, rather than extend, the production selector. Extending it would
	// leave a clone eligible for the production Service selector.
	spec["selector"] = map[string]any{SessionLabel: session, WorkloadLabel: workload}
	return json.Marshal(object)
}

// AddWorkloadLabel makes the cloned deployment and its pod selector unique
// within a session, so sibling cloned Services cannot select one another.
func AddWorkloadLabel(manifest []byte, workload string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(manifest, &object); err != nil {
		return nil, err
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment spec is required")
	}
	selector := ensureMap(spec, "selector")
	ensureMap(selector, "matchLabels")[WorkloadLabel] = workload
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment pod template is required")
	}
	ensureMap(ensureMap(template, "metadata"), "labels")[WorkloadLabel] = workload
	return json.Marshal(object)
}

// SetContainerEnv changes one literal environment value on the named container.
// Stack deployments use it to give their cloned Dashboard a test-specific public
// hostname without changing the managed Dashboard configuration.
func SetContainerEnv(manifest []byte, containerName, variable, value string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(manifest, &object); err != nil {
		return nil, fmt.Errorf("parse deployment: %w", err)
	}
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment spec is required")
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment template is required")
	}
	podSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("pod spec is required")
	}
	containers, ok := podSpec["containers"].([]any)
	if !ok {
		return nil, fmt.Errorf("deployment containers are required")
	}
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok || container["name"] != containerName {
			continue
		}
		env, ok := container["env"].([]any)
		if !ok {
			return nil, fmt.Errorf("container %q has no environment entries", containerName)
		}
		for _, envItem := range env {
			entry, ok := envItem.(map[string]any)
			if ok && entry["name"] == variable {
				entry["value"] = value
				delete(entry, "valueFrom")
				return json.Marshal(object)
			}
		}
		return nil, fmt.Errorf("container %q has no %s environment entry", containerName, variable)
	}
	return nil, fmt.Errorf("container %q not found", containerName)
}

// StackFederationManifest clones the runtime federation configuration and directs
// only the cloned host's GenAI and core-BFF requests at the cloned Services.
func StackFederationManifest(data []byte, name, session, componentService, dashboardService string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("parse federation configmap: %w", err)
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("configmap metadata is required")
	}
	metadata["name"] = name
	for _, key := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "ownerReferences", "annotations"} {
		delete(metadata, key)
	}
	ensureMap(metadata, "labels")[SessionLabel] = session
	dataMap, ok := object["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("configmap data is required")
	}
	raw, ok := dataMap["module-federation-config.json"].(string)
	if !ok {
		return nil, fmt.Errorf("module-federation-config.json is required")
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse federation config: %w", err)
	}
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		if name != "genAi" && name != "coreBff" {
			continue
		}
		for _, key := range []string{"service", "proxyService"} {
			items, ok := entry[key].([]any)
			if !ok {
				continue
			}
			for _, item := range items {
				if route, ok := item.(map[string]any); ok {
					if service, ok := route["service"].(map[string]any); ok && name == "coreBff" {
						service["name"] = dashboardService
					}
				}
			}
		}
		if service, ok := entry["service"].(map[string]any); ok && name == "genAi" {
			service["name"] = componentService
		}
	}
	updated, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	dataMap["module-federation-config.json"] = string(updated)
	return json.Marshal(object)
}

func StackRouteManifest(name, namespace, session, hostname, service string) ([]byte, error) {
	if hostname == "" {
		return nil, fmt.Errorf("route hostname is required")
	}
	return json.Marshal(map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "labels": map[string]string{SessionLabel: session}},
		"spec": map[string]any{
			"hostnames":  []string{hostname},
			"parentRefs": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": "data-science-gateway", "namespace": "openshift-ingress"}},
			"rules": []any{map[string]any{
				"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}},
				"backendRefs": []any{map[string]any{"group": "", "kind": "Service", "name": service, "port": 8443, "weight": 1}},
			}},
		},
	})
}

// StackIngressRouteManifest bridges a public wildcard host to the existing
// authenticated data-science Gateway. It is a new Route only; no gateway-owned
// Route, Service, or policy is changed.
func StackIngressRouteManifest(name, session, hostname string) ([]byte, error) {
	if hostname == "" {
		return nil, fmt.Errorf("ingress route hostname is required")
	}
	return json.Marshal(map[string]any{
		"apiVersion": "route.openshift.io/v1", "kind": "Route",
		"metadata": map[string]any{"name": name, "namespace": "openshift-ingress", "labels": map[string]string{SessionLabel: session}},
		"spec":     map[string]any{"host": hostname, "to": map[string]any{"kind": "Service", "name": "data-science-gateway-data-science-gateway-class", "weight": 100}, "port": map[string]any{"targetPort": 443}, "tls": map[string]any{"termination": "reencrypt", "insecureEdgeTerminationPolicy": "Redirect"}},
	})
}

func MarshalSession(session Session) ([]byte, error) { return json.MarshalIndent(session, "", "  ") }
func UnmarshalSession(data []byte) (Session, error) {
	var session Session
	err := json.Unmarshal(data, &session)
	return session, err
}

// RestorePatch produces a strategic-merge patch. The currentImage parameter documents the value
// expected by cleanup; validation is performed before this patch is submitted.
func RestorePatch(container, variable string, original ImageVariable, currentImage string) ([]byte, error) {
	if container == "" || variable == "" || currentImage == "" {
		return nil, fmt.Errorf("container, variable, and current image are required")
	}
	env := map[string]string{"name": variable}
	if original.Present {
		env["value"] = original.Value
	} else {
		env["$patch"] = "delete"
	}
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": container, "env": []any{env}}},
	}}}}
	return json.Marshal(patch)
}

func UpdatePatch(container, variable, image string) ([]byte, error) {
	if container == "" || variable == "" || image == "" {
		return nil, fmt.Errorf("container, variable, and image are required")
	}
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": container, "env": []any{map[string]string{"name": variable, "value": image}}}},
	}}}}
	return json.Marshal(patch)
}

func DeploymentImagePatch(container, image string) ([]byte, error) {
	if container == "" || image == "" {
		return nil, fmt.Errorf("container and image are required")
	}
	patch := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]string{"name": container, "image": image}},
	}}}}
	return json.Marshal(patch)
}

func ValidateCleanupImage(current, sessionImage, original string) error {
	if current == original {
		return nil
	}
	if current != sessionImage {
		return fmt.Errorf("refusing cleanup: image override changed outside session (current %q, session %q)", current, sessionImage)
	}
	return nil
}

type Metadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	UID             string            `json:"uid,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	OwnerReferences []json.RawMessage `json:"ownerReferences,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}
type PodSpec struct {
	Containers []Container `json:"containers"`
}
type PodTemplate struct {
	Metadata Metadata `json:"metadata"`
	Spec     PodSpec  `json:"spec"`
}
type DeploymentSpec struct {
	Selector map[string]any `json:"selector,omitempty"`
	Template PodTemplate    `json:"template"`
}
type Deployment struct {
	APIVersion string         `json:"apiVersion,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Metadata   Metadata       `json:"metadata"`
	Spec       DeploymentSpec `json:"spec"`
}

func ShadowDeployment(data []byte, name, container, image, session string) (Deployment, error) {
	var deployment Deployment
	if err := json.Unmarshal(data, &deployment); err != nil {
		return Deployment{}, fmt.Errorf("parse deployment: %w", err)
	}
	if deployment.Metadata.Name == "" {
		return Deployment{}, fmt.Errorf("deployment metadata.name is required")
	}
	deployment.APIVersion = "apps/v1"
	deployment.Kind = "Deployment"
	deployment.Metadata.Name = name
	deployment.Metadata.UID = ""
	deployment.Metadata.ResourceVersion = ""
	deployment.Metadata.OwnerReferences = nil
	if deployment.Metadata.Labels == nil {
		deployment.Metadata.Labels = map[string]string{}
	}
	delete(deployment.Metadata.Labels, "platform.opendatahub.io/part-of")
	deployment.Metadata.Labels[SessionLabel] = session
	if deployment.Spec.Template.Metadata.Labels == nil {
		deployment.Spec.Template.Metadata.Labels = map[string]string{}
	}
	delete(deployment.Spec.Template.Metadata.Labels, "platform.opendatahub.io/part-of")
	deployment.Spec.Template.Metadata.Labels[SessionLabel] = session
	if deployment.Spec.Selector == nil {
		deployment.Spec.Selector = map[string]any{}
	}
	matchLabels, ok := deployment.Spec.Selector["matchLabels"].(map[string]any)
	if !ok || matchLabels == nil {
		matchLabels = map[string]any{}
		deployment.Spec.Selector["matchLabels"] = matchLabels
	}
	matchLabels[SessionLabel] = session
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == container {
			deployment.Spec.Template.Spec.Containers[i].Image = image
			return deployment, nil
		}
	}
	return Deployment{}, fmt.Errorf("container %q not found", container)
}

// ShadowManifest preserves the source workload's complete PodSpec (volumes, service account,
// security context, probes, and affinity) while removing controller-owned identity fields.
func ShadowManifest(data []byte, name, container, image, session string) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("parse deployment: %w", err)
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment metadata is required")
	}
	metadata["name"] = name
	for _, key := range []string{"uid", "resourceVersion", "creationTimestamp", "generation", "managedFields", "ownerReferences", "annotations"} {
		delete(metadata, key)
	}
	labels := ensureMap(metadata, "labels")
	delete(labels, "platform.opendatahub.io/part-of")
	labels[SessionLabel] = session
	delete(object, "status")
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment spec is required")
	}
	spec["selector"] = map[string]any{"matchLabels": map[string]any{SessionLabel: session}}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment pod template is required")
	}
	templateMetadata := ensureMap(template, "metadata")
	// Do not retain workload selector labels (for example deployment=gen-ai-ui):
	// production Services must never be able to select this test pod.
	templateMetadata["labels"] = map[string]any{SessionLabel: session}
	templateSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment pod spec is required")
	}
	containers, ok := templateSpec["containers"].([]any)
	if !ok {
		return nil, fmt.Errorf("deployment containers are required")
	}
	for _, item := range containers {
		value, ok := item.(map[string]any)
		if ok && value["name"] == container {
			value["image"] = image
			return json.Marshal(object)
		}
	}
	return nil, fmt.Errorf("container %q not found", container)
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok && value != nil {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}
