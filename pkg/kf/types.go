// Package kf contains the minimal subset of Kubeflow Trainer v2 (v1alpha1)
// types forgeai needs. Objects are converted to unstructured and
// applied with server-side apply, so only fields we set are owned by forgeai.
package kf

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group and Version of the Kubeflow Trainer v2 API.
const (
	Group   = "trainer.kubeflow.org"
	Version = "v1alpha1"
)

var (
	TrainJobGVK        = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "TrainJob"}
	TrainingRuntimeGVK = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "TrainingRuntime"}
)

const (
	LabelFramework     = "trainer.kubeflow.org/framework"
	LabelAncestorStep  = "trainer.kubeflow.org/trainjob-ancestor-step"
	AncestorTrainer    = "trainer"
	LabelKueueQueue    = "kueue.x-k8s.io/queue-name"
	AnnotationNetworks = "k8s.v1.cni.cncf.io/networks"

	ConditionComplete  = "Complete"
	ConditionFailed    = "Failed"
	ConditionSuspended = "Suspended"
)

type TrainingRuntime struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TrainingRuntimeSpec `json:"spec"`
}

type TrainingRuntimeSpec struct {
	MLPolicy *MLPolicy          `json:"mlPolicy,omitempty"`
	Template JobSetTemplateSpec `json:"template"`
}

type MLPolicy struct {
	NumNodes *int32    `json:"numNodes,omitempty"`
	Torch    *struct{} `json:"torch,omitempty"`
}

type JobSetTemplateSpec struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              JobSetSpec `json:"spec"`
}

type JobSetSpec struct {
	ReplicatedJobs          []ReplicatedJob `json:"replicatedJobs"`
	TTLSecondsAfterFinished *int32          `json:"ttlSecondsAfterFinished,omitempty"`
}

type ReplicatedJob struct {
	Name     string                  `json:"name"`
	Replicas int32                   `json:"replicas"`
	Template batchv1.JobTemplateSpec `json:"template"`
}

type TrainJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TrainJobSpec    `json:"spec"`
	Status            *TrainJobStatus `json:"status,omitempty"`
}

type TrainJobSpec struct {
	RuntimeRef RuntimeRef `json:"runtimeRef"`
	Trainer    *Trainer   `json:"trainer,omitempty"`
	Suspend    *bool      `json:"suspend,omitempty"`
}

type RuntimeRef struct {
	APIGroup string `json:"apiGroup,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name"`
}

type Trainer struct {
	NumProcPerNode *int32 `json:"numProcPerNode,omitempty"`
}

type TrainJobStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	JobsStatus []JobStatus        `json:"jobsStatus,omitempty"`
}

type JobStatus struct {
	Name      string `json:"name"`
	Active    int32  `json:"active"`
	Ready     int32  `json:"ready"`
	Succeeded int32  `json:"succeeded"`
	Failed    int32  `json:"failed"`
	Suspended int32  `json:"suspended"`
}
