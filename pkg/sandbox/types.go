// Package sandbox contains the subset of the Kubernetes SIG Apps
// agent-sandbox API (agents.x-k8s.io/v1beta1) forgeai uses, plus the rendering
// and status rules shared by the operator and the GUI. Objects are exchanged
// as unstructured, so only the fields forgeai sets or reads are modelled.
package sandbox

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group and Version of the agent-sandbox core and extension APIs.
const (
	Group   = "agents.x-k8s.io"
	Version = "v1beta1"
	// ExtensionsGroup holds SandboxTemplate, SandboxClaim and SandboxWarmPool.
	ExtensionsGroup = "extensions.agents.x-k8s.io"
)

var (
	SandboxGVK         = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "Sandbox"}
	SandboxTemplateGVK = schema.GroupVersionKind{Group: ExtensionsGroup, Version: Version, Kind: "SandboxTemplate"}
	SandboxClaimGVK    = schema.GroupVersionKind{Group: ExtensionsGroup, Version: Version, Kind: "SandboxClaim"}
	SandboxWarmPoolGVK = schema.GroupVersionKind{Group: ExtensionsGroup, Version: Version, Kind: "SandboxWarmPool"}
	// HTTPRouteGVK is the Gateway API route that publishes a sandbox port.
	HTTPRouteGVK = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
)

// Labels and annotations set by the agent-sandbox controller.
const (
	// LabelManagedBy is set on the pods and services of a sandbox.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelSandboxName is the sandbox a pod or service belongs to.
	LabelSandboxName = "agents.x-k8s.io/sandbox-name"
	// LabelTemplateRef is set on sandboxes created from a template.
	LabelTemplateRef = "agents.x-k8s.io/sandbox-template-ref"
	// AnnotationPodName is the backing pod of a legacy (warm pool) sandbox.
	AnnotationPodName = "agents.x-k8s.io/pod-name"
	// LabelSandboxNameHash is set on the pods of a sandbox and selects them
	// from the sandbox's own Service.
	LabelSandboxNameHash = "agents.x-k8s.io/sandbox-name-hash"
)

// Condition types and the reasons forgeai distinguishes.
const (
	ConditionReady     = "Ready"
	ConditionSuspended = "Suspended"

	ReasonDependenciesReady    = "DependenciesReady"
	ReasonDependenciesNotReady = "DependenciesNotReady"
	ReasonReconcilerError      = "ReconcilerError"
	ReasonInvalidConfiguration = "InvalidConfiguration"
	ReasonMultiplePods         = "MultiplePods"
	ReasonPodScheduled         = "PodScheduled"
	ReasonPodSucceeded         = "PodSucceeded"
	ReasonPodFailed            = "PodFailed"
	ReasonExpired              = "SandboxExpired"
	ReasonSuspended            = "SandboxSuspended"
	ReasonNotSuspended         = "NotSuspended"
	ReasonPodTerminated        = "PodTerminated"
	ReasonPodTerminating       = "PodTerminating"
)

// OperatingMode says whether the controller keeps a pod for a sandbox.
type OperatingMode string

const (
	OperatingRunning   OperatingMode = "Running"
	OperatingSuspended OperatingMode = "Suspended"
)

// ShutdownPolicy is what happens when a sandbox reaches its shutdownTime.
type ShutdownPolicy string

const (
	ShutdownDelete ShutdownPolicy = "Delete"
	ShutdownRetain ShutdownPolicy = "Retain"
)

// PodMetadata carries the labels and annotations copied onto the pod.
type PodMetadata struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// PodTemplate is the pod of a sandbox.
type PodTemplate struct {
	Spec       corev1.PodSpec `json:"spec"`
	ObjectMeta PodMetadata    `json:"metadata,omitempty"`
}

// PersistentVolumeClaimTemplate is a volume created with the sandbox and
// deleted with it.
type PersistentVolumeClaimTemplate struct {
	ObjectMeta struct {
		Name        string            `json:"name,omitempty"`
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	} `json:"metadata,omitempty"`
	Spec corev1.PersistentVolumeClaimSpec `json:"spec"`
}

// Blueprint is what a sandbox (and a SandboxTemplate) is made of.
type Blueprint struct {
	PodTemplate          PodTemplate                     `json:"podTemplate"`
	VolumeClaimTemplates []PersistentVolumeClaimTemplate `json:"volumeClaimTemplates,omitempty"`
	// Service creates a stable Service for the pod (default true server-side).
	Service *bool `json:"service,omitempty"`
}

// Lifecycle stops a sandbox at shutdownTime; Delete removes it, Retain only
// deletes the pod (the default server-side is Delete).
type Lifecycle struct {
	ShutdownTime   *metav1.Time    `json:"shutdownTime,omitempty"`
	ShutdownPolicy *ShutdownPolicy `json:"shutdownPolicy,omitempty"`
}

// SandboxSpec is the spec of a Sandbox.
type SandboxSpec struct {
	Blueprint `json:",inline"`
	Lifecycle `json:",inline"`
	// OperatingMode suspends the sandbox (pod deleted, spec kept).
	OperatingMode OperatingMode `json:"operatingMode,omitempty"`
}

// SandboxStatus is what the controller observed.
type SandboxStatus struct {
	ServiceFQDN string             `json:"serviceFQDN,omitempty"`
	Service     string             `json:"service,omitempty"`
	Conditions  []metav1.Condition `json:"conditions,omitempty"`
	Selector    string             `json:"selector,omitempty"`
	PodIPs      []string           `json:"podIPs,omitempty"`
	NodeName    string             `json:"nodeName,omitempty"`
}

// Sandbox is one isolated, stateful, singleton workload.
type Sandbox struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SandboxSpec   `json:"spec"`
	Status            SandboxStatus `json:"status,omitempty"`
}

// SandboxList is a list of Sandboxes.
type SandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Sandbox `json:"items"`
}

// FromUnstructured converts an unstructured object into a typed one.
func FromUnstructured(obj map[string]any, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(obj, out)
}
