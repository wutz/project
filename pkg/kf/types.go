// Package kf contains the subset of the Kubeflow Trainer v2 (v1alpha1) and
// Kueue APIs forgeai uses. Objects are exchanged as unstructured, so only the
// fields forgeai sets or reads are modelled.
package kf

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group and Version of the Kubeflow Trainer v2 API.
const (
	Group   = "trainer.kubeflow.org"
	Version = "v1alpha1"
)

var (
	TrainJobGVK               = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "TrainJob"}
	TrainingRuntimeGVK        = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "TrainingRuntime"}
	ClusterTrainingRuntimeGVK = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "ClusterTrainingRuntime"}

	KueueGroupVersion  = schema.GroupVersion{Group: "kueue.x-k8s.io", Version: "v1beta2"}
	WorkloadGVK        = KueueGroupVersion.WithKind("Workload")
	LocalQueueGVK      = KueueGroupVersion.WithKind("LocalQueue")
	ClusterQueueGVK    = KueueGroupVersion.WithKind("ClusterQueue")
	ResourceFlavorGVK  = KueueGroupVersion.WithKind("ResourceFlavor")
	JobSetGroupVersion = schema.GroupVersion{Group: "jobset.x-k8s.io", Version: "v1alpha2"}
)

const (
	LabelFramework    = "trainer.kubeflow.org/framework"
	LabelAncestorStep = "trainer.kubeflow.org/trainjob-ancestor-step"
	AncestorTrainer   = "trainer"
	// ContainerNode is the trainer container of the torch runtimes.
	ContainerNode = "node"

	LabelKueueQueue  = "kueue.x-k8s.io/queue-name"
	LabelKueueJobUID = "kueue.x-k8s.io/job-uid"
	LabelJobSetName  = "jobset.sigs.k8s.io/jobset-name"
	LabelCompletion  = "batch.kubernetes.io/job-completion-index"

	// PatchManager identifies forgeai's entry in TrainJob runtimePatches.
	PatchManager = "forgeai.wutz.dev/forgeai"

	ConditionComplete  = "Complete"
	ConditionFailed    = "Failed"
	ConditionSuspended = "Suspended"
)

// TrainingRuntime is a namespaced runtime; forgeai job templates are TrainingRuntimes.
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
	ReplicatedJobs []ReplicatedJob `json:"replicatedJobs"`
}

type ReplicatedJob struct {
	Name     string                  `json:"name"`
	Replicas int32                   `json:"replicas"`
	Template batchv1.JobTemplateSpec `json:"template"`
}

// TrainJob is a training run.
type TrainJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TrainJobSpec    `json:"spec"`
	Status            *TrainJobStatus `json:"status,omitempty"`
}

type TrainJobSpec struct {
	RuntimeRef     RuntimeRef     `json:"runtimeRef"`
	Trainer        *Trainer       `json:"trainer,omitempty"`
	RuntimePatches []RuntimePatch `json:"runtimePatches,omitempty"`
	Suspend        *bool          `json:"suspend,omitempty"`
}

type RuntimeRef struct {
	APIGroup string `json:"apiGroup,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name"`
}

type Trainer struct {
	Image            string                       `json:"image,omitempty"`
	Command          []string                     `json:"command,omitempty"`
	Args             []string                     `json:"args,omitempty"`
	Env              []corev1.EnvVar              `json:"env,omitempty"`
	NumNodes         *int32                       `json:"numNodes,omitempty"`
	NumProcPerNode   *int32                       `json:"numProcPerNode,omitempty"`
	ResourcesPerNode *corev1.ResourceRequirements `json:"resourcesPerNode,omitempty"`
}

// RuntimePatch is one manager's patch of the runtime (listMapKey=manager).
type RuntimePatch struct {
	Manager             string                    `json:"manager"`
	TrainingRuntimeSpec *TrainingRuntimeSpecPatch `json:"trainingRuntimeSpec,omitempty"`
}

type TrainingRuntimeSpecPatch struct {
	Template *JobSetTemplatePatch `json:"template,omitempty"`
}

type JobSetTemplatePatch struct {
	Spec *JobSetSpecPatch `json:"spec,omitempty"`
}

type JobSetSpecPatch struct {
	ReplicatedJobs []ReplicatedJobPatch `json:"replicatedJobs,omitempty"`
}

type ReplicatedJobPatch struct {
	Name     string            `json:"name"`
	Template *JobTemplatePatch `json:"template,omitempty"`
}

type JobTemplatePatch struct {
	Spec *JobSpecPatch `json:"spec,omitempty"`
}

type JobSpecPatch struct {
	Template *PodTemplatePatch `json:"template,omitempty"`
}

type PodTemplatePatch struct {
	Metadata *metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec     *PodSpecPatch      `json:"spec,omitempty"`
}

// PodSpecPatch holds the pod fields a TrainJob may patch.
type PodSpecPatch struct {
	NodeSelector     map[string]string             `json:"nodeSelector,omitempty"`
	Tolerations      []corev1.Toleration           `json:"tolerations,omitempty"`
	Volumes          []corev1.Volume               `json:"volumes,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	Containers       []ContainerPatch              `json:"containers,omitempty"`
}

type ContainerPatch struct {
	Name         string               `json:"name"`
	Env          []corev1.EnvVar      `json:"env,omitempty"`
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
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

// FromUnstructured converts an unstructured object into a typed one.
func FromUnstructured(obj map[string]any, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(obj, out)
}
