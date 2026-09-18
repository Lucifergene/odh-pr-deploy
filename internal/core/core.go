package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

const SessionLabel = "odh-pr-deploy.opendatahub.io/session"

// Component describes the operator-owned dashboard workload whose image may be tested.
type Component struct {
	Name       string `json:"name"`
	Deployment string `json:"deployment"`
	Container  string `json:"container"`
	ImageEnv   string `json:"imageEnv"`
}

var components = map[string]Component{
	"gen-ai": {
		Name:       "gen-ai",
		Deployment: "gen-ai-ui",
		Container:  "gen-ai-ui",
		ImageEnv:   "RELATED_IMAGE_ODH_MOD_ARCH_GEN_AI_IMAGE",
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
	Completed          bool          `json:"completed"`
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
	selector := ensureMap(spec, "selector")
	matchLabels := ensureMap(selector, "matchLabels")
	matchLabels[SessionLabel] = session
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("deployment pod template is required")
	}
	templateMetadata := ensureMap(template, "metadata")
	templateLabels := ensureMap(templateMetadata, "labels")
	delete(templateLabels, "platform.opendatahub.io/part-of")
	templateLabels[SessionLabel] = session
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
