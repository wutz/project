package sandbox

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The extension objects (SandboxTemplate, SandboxClaim, SandboxWarmPool) are
// modelled only as far as forgeai reads them: the GUI lists templates, claims
// and pools, and the platform keeps a warm pool of its default template.
// Members create them with kubectl or the GUI; the agent-sandbox extensions
// controller does the work.

// SandboxTemplate is a reusable sandbox blueprint.
type SandboxTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Blueprint `json:"spec"`
}

// SandboxTemplateList is a list of SandboxTemplates.
type SandboxTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxTemplate `json:"items"`
}

// SandboxWarmPoolRef names the pool a claim takes a sandbox from.
type SandboxWarmPoolRef struct {
	Name string `json:"name"`
}

// ClaimLifecycle stops the claimed sandbox.
type ClaimLifecycle struct {
	ShutdownTime            *metav1.Time `json:"shutdownTime,omitempty"`
	TTLSecondsAfterFinished *int32       `json:"ttlSecondsAfterFinished,omitempty"`
	ShutdownPolicy          string       `json:"shutdownPolicy,omitempty"`
}

// SandboxClaimSpec claims a sandbox from a warm pool.
type SandboxClaimSpec struct {
	WarmPoolRef SandboxWarmPoolRef `json:"warmPoolRef"`
	Lifecycle   *ClaimLifecycle    `json:"lifecycle,omitempty"`
}

// ClaimSandboxStatus points at the sandbox a claim received.
type ClaimSandboxStatus struct {
	Name        string   `json:"name,omitempty"`
	PodIPs      []string `json:"podIPs,omitempty"`
	ServiceFQDN string   `json:"serviceFQDN,omitempty"`
}

// SandboxClaimStatus is the observed state of a claim.
type SandboxClaimStatus struct {
	Conditions    []metav1.Condition `json:"conditions,omitempty"`
	SandboxStatus ClaimSandboxStatus `json:"sandbox,omitempty"`
}

// SandboxClaim is a request for a warm sandbox.
type SandboxClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SandboxClaimSpec   `json:"spec"`
	Status            SandboxClaimStatus `json:"status,omitempty"`
}

// SandboxClaimList is a list of SandboxClaims.
type SandboxClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxClaim `json:"items"`
}

// SandboxTemplateRef names the template a pool is built from.
type SandboxTemplateRef struct {
	Name string `json:"name"`
}

// SandboxWarmPoolSpec keeps replicas sandboxes of a template ready.
type SandboxWarmPoolSpec struct {
	Replicas    *int32             `json:"replicas,omitempty"`
	TemplateRef SandboxTemplateRef `json:"sandboxTemplateRef"`
}

// SandboxWarmPoolStatus is the observed state of a pool.
type SandboxWarmPoolStatus struct {
	Replicas           int32  `json:"replicas,omitempty"`
	ReadyReplicas      int32  `json:"readyReplicas,omitempty"`
	Selector           string `json:"selector,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

// SandboxWarmPool keeps pre-warmed sandboxes ready.
type SandboxWarmPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SandboxWarmPoolSpec   `json:"spec"`
	Status            SandboxWarmPoolStatus `json:"status,omitempty"`
}

// SandboxWarmPoolList is a list of SandboxWarmPools.
type SandboxWarmPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SandboxWarmPool `json:"items"`
}
