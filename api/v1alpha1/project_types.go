/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// GPUQuota is the number of GPUs of one class a project may run at once.
type GPUQuota struct {
	// class is the InstanceType GPU class, e.g. rtx-5090.
	// +required
	// +kubebuilder:validation:MinLength=1
	Class string `json:"class"`
	// +required
	// +kubebuilder:validation:Minimum=0
	Count int32 `json:"count"`
}

// ProjectQuota limits what a project may run and store. Unset fields take the
// platform defaults.
type ProjectQuota struct {
	// gpus is the GPU quota per GPU class. CPU and memory quotas follow from the
	// per-GPU slice of each class. Jobs over quota wait in the queue.
	// +optional
	// +listType=map
	// +listMapKey=class
	GPUs []GPUQuota `json:"gpus,omitempty"`
	// storage is the total requested size of all volumes.
	// +optional
	Storage *resource.Quantity `json:"storage,omitempty"`
	// volumes is the maximum number of volumes (PVCs).
	// +optional
	Volumes *int32 `json:"volumes,omitempty"`
	// pods is the maximum number of pods.
	// +optional
	Pods *int32 `json:"pods,omitempty"`
	// registryStorage is the Harbor project storage quota; empty is unlimited.
	// +optional
	RegistryStorage *resource.Quantity `json:"registryStorage,omitempty"`
}

// NetworkIsolation selects the network policy of a project namespace.
// +kubebuilder:validation:Enum=Isolated;None
type NetworkIsolation string

const (
	// NetworkIsolated only admits traffic from the same project and platform namespaces.
	NetworkIsolated NetworkIsolation = "Isolated"
	// NetworkNone applies no policy.
	NetworkNone NetworkIsolation = "None"
)

// ProjectNetwork configures network isolation.
type ProjectNetwork struct {
	// isolation defaults to Isolated.
	// +optional
	// +kubebuilder:default=Isolated
	Isolation NetworkIsolation `json:"isolation,omitempty"`
	// denyInternet blocks egress outside the project (DNS stays allowed).
	// +optional
	DenyInternet bool `json:"denyInternet,omitempty"`
}

// MemberRole is the access level of a project member. Members are managed by
// platform admins through the Project, so there is no project admin role.
// +kubebuilder:validation:Enum=edit;view
type MemberRole string

const (
	// RoleEdit manages jobs, templates, volumes, files and images.
	RoleEdit MemberRole = "edit"
	// RoleView is read-only and cannot read Secrets.
	RoleView MemberRole = "view"
)

// Member is a user of the project. Each member gets a ServiceAccount in the
// platform members namespace and a token that the admin exports as a kubeconfig.
type Member struct {
	// name identifies the member within the project.
	// +required
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +optional
	// +kubebuilder:default=edit
	Role MemberRole `json:"role,omitempty"`
}

// DeletionPolicy decides what happens to project data when the Project is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type DeletionPolicy string

const (
	// DeletionRetain revokes access but keeps the namespace and registry project.
	DeletionRetain DeletionPolicy = "Retain"
	// DeletionDelete removes the namespace (with all volumes) and registry project.
	DeletionDelete DeletionPolicy = "Delete"
)

// ProjectSpec defines the desired state of Project.
type ProjectSpec struct {
	// displayName is shown in the GUI.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// +optional
	Quota ProjectQuota `json:"quota,omitempty"`
	// +optional
	Network ProjectNetwork `json:"network,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=name
	Members []Member `json:"members,omitempty"`
	// trashRetention is how long deleted volumes are kept before they are
	// purged, unless a volume sets its own expiry. Defaults to the platform setting.
	// +optional
	TrashRetention *metav1.Duration `json:"trashRetention,omitempty"`
	// deletionPolicy defaults to Retain.
	// +optional
	// +kubebuilder:default=Retain
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// MemberStatus reports the credentials of a member.
type MemberStatus struct {
	Name string `json:"name"`
	// namespace of the ServiceAccount (the platform members namespace).
	Namespace string `json:"namespace"`
	// serviceAccount of the member.
	ServiceAccount string `json:"serviceAccount"`
	// tokenSecret holds the long-lived token (kubernetes.io/service-account-token).
	TokenSecret string `json:"tokenSecret"`
}

// RegistryStatus reports the Harbor project of a project.
type RegistryStatus struct {
	// project is the Harbor project name.
	Project string `json:"project,omitempty"`
	// url is the push prefix, e.g. cr.example.com:7443/forge-foo.
	URL string `json:"url,omitempty"`
	// secret in the project namespace holds the robot account (dockerconfigjson).
	Secret string `json:"secret,omitempty"`
}

// ProjectStatus defines the observed state of Project.
type ProjectStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// namespace backing the project.
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// clusterQueue is the Kueue ClusterQueue enforcing the GPU quota.
	// +optional
	ClusterQueue string `json:"clusterQueue,omitempty"`
	// localQueue is the queue jobs in the namespace submit to.
	// +optional
	LocalQueue string `json:"localQueue,omitempty"`
	// +optional
	Registry RegistryStatus `json:"registry,omitzero"`
	// +optional
	Members []MemberStatus `json:"members,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition types of a Project.
const (
	ConditionReady    = "Ready"
	ConditionRegistry = "RegistryReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fproj,categories=project
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.status.namespace`
// +kubebuilder:printcolumn:name="Isolation",type=string,JSONPath=`.spec.network.isolation`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Registry",type=string,JSONPath=`.status.conditions[?(@.type=="RegistryReady")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$') && size(self.metadata.name) <= 40",message="project name must be a DNS label of at most 40 characters"

// Project is a tenant: a labelled namespace with quota, network isolation,
// a Kueue queue, a Harbor project and member credentials.
type Project struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Project
	// +required
	Spec ProjectSpec `json:"spec"`

	// status defines the observed state of Project
	// +optional
	Status ProjectStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProjectList contains a list of Project
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Project `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Project{}, &ProjectList{})
		return nil
	})
}
