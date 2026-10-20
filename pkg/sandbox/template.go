package sandbox

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/wutz/forgeai/pkg/naming"
)

// TemplateForm is a sandbox template as entered in the GUI: the agent a
// sandbox starts from. Sizing, placement, volumes and lifetime are chosen per
// sandbox, so they are not part of a template.
type TemplateForm struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Image runs the agent; the platform default is used when empty.
	Image string `json:"image"`
	// Command is a bash script run instead of the image entrypoint.
	Command string `json:"command,omitempty"`
	// Env is set on the container.
	Env []EnvVar `json:"env,omitempty"`
	// RuntimeClass isolates the sandbox (gvisor, kata); empty uses the node runtime.
	RuntimeClass string `json:"runtimeClass,omitempty"`
}

// Validate checks a template form. The image may still be empty here: it is
// defaulted from the platform settings in RenderTemplate.
func (f *TemplateForm) Validate() error {
	if !nameRE.MatchString(f.Name) || len(f.Name) > MaxNameLength {
		return fmt.Errorf("invalid name %q: lowercase letters, digits and '-', starting with a letter, at most %d characters",
			f.Name, MaxNameLength)
	}
	for _, e := range f.Env {
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("environment variables need a name")
		}
	}
	return nil
}

// RenderTemplate builds the SandboxTemplate of a form. The pod it describes
// matches what Render builds for a sandbox, minus the per-sandbox resources.
func RenderTemplate(f *TemplateForm, c Context) (*unstructured.Unstructured, error) {
	cfg := c.Platform
	if f.Image == "" && cfg != nil {
		f.Image = cfg.Sandbox.DefaultImage
	}
	if strings.TrimSpace(f.Image) == "" {
		return nil, fmt.Errorf("image is required")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	runtimeClass := f.RuntimeClass
	if runtimeClass != "" && cfg != nil && !cfg.Sandbox.HasRuntimeClass(runtimeClass) {
		return nil, fmt.Errorf("runtime class %q is not offered by the platform", runtimeClass)
	}
	pod := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyAlways,
		Containers: []corev1.Container{{
			Name:    ContainerName,
			Image:   f.Image,
			Command: command(f.Command),
			Env:     env(f.Env),
		}},
	}
	if runtimeClass != "" {
		pod.RuntimeClassName = &runtimeClass
	}
	t := &SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.Name,
			Namespace: c.Namespace,
			Labels: map[string]string{
				naming.LabelManagedBy: naming.ManagedBy,
				naming.LabelComponent: naming.ComponentSandbox,
			},
		},
		Spec: Blueprint{PodTemplate: PodTemplate{Spec: pod}},
	}
	if c.Project != "" {
		t.Labels[naming.LabelProject] = c.Project
	}
	if d := strings.TrimSpace(f.Description); d != "" {
		t.Annotations = map[string]string{naming.AnnotationDescription: d}
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(t)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(SandboxTemplateGVK)
	unstructured.RemoveNestedField(u.Object, naming.KeyMetadata, "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, naming.KeyStatus)
	return u, nil
}

// TemplateFormOf rebuilds a template form from a SandboxTemplate, for the
// "New from template" prefill in the GUI.
func TemplateFormOf(t *SandboxTemplate) *TemplateForm {
	f := &TemplateForm{
		Name:         t.Name,
		Description:  t.Annotations[naming.AnnotationDescription],
		RuntimeClass: ptrString(t.Spec.PodTemplate.Spec.RuntimeClassName),
	}
	for _, ctr := range t.Spec.PodTemplate.Spec.Containers {
		if ctr.Name != ContainerName {
			continue
		}
		f.Image = ctr.Image
		f.Command = scriptOf(ctr.Command)
		for _, e := range ctr.Env {
			f.Env = append(f.Env, EnvVar{Name: e.Name, Value: e.Value})
		}
	}
	return f
}
