// Package naming holds object names and label keys shared by the operator,
// the compat API and the GUI. Names follow scc-k8s-v2 so job IDs and PVCs
// stay compatible.
package naming

import (
	"fmt"
	"strconv"
	"strings"
)

// Label keys (compatible with scc-k8s-v2).
const (
	LabelAppName      = "app.kubernetes.io/name"
	LabelAppInstance  = "app.kubernetes.io/instance"
	LabelAppVersion   = "app.kubernetes.io/version"
	LabelManagedBy    = "app.kubernetes.io/managed-by"
	LabelUserID       = "user.scclabs.dev/id"
	LabelUserGroup    = "user.scclabs.dev/group"
	LabelPodID        = "pod.scclabs.dev/id"
	LabelPodName      = "pod.scclabs.dev/name"
	LabelResourceID   = "pod.scclabs.dev/resource-id"
	LabelTrainerJobID = "pod.scclabs.dev/trainer-job-id"
	LabelGPU          = "scclabs.dev/gpu"
	LabelCompletion   = "batch.kubernetes.io/job-completion-index"
	LabelVolumeID     = "pod.scclabs.dev/volume-id"
	LabelJobName      = "job-name"

	// LabelTrainingJob is set on every object owned by a TrainingJob (value = CR name).
	LabelTrainingJob = "forgeai.scclabs.dev/training-job"

	// LabelComponent marks every object the operator manages (training,
	// image-warmup); the operator caches only these.
	LabelComponent = "forgeai.scclabs.dev/component"
	// LabelImageWarmup is the warmup ID (hash of the image) on warmup DaemonSets.
	LabelImageWarmup = "forgeai.scclabs.dev/image-warmup"
	// AnnotationExpires is when a warmup DaemonSet is removed (RFC3339).
	AnnotationExpires = "forgeai.scclabs.dev/expires"
	// AnnotationImage is the image a warmup DaemonSet pulls.
	AnnotationImage = "forgeai.scclabs.dev/image"

	// AnnotationRestartGeneration records which restartGeneration created a TrainJob.
	AnnotationRestartGeneration = "forgeai.scclabs.dev/restart-generation"

	AppNameTrainingJob = "forgeai-training"
	AppNameVolume      = "volume"
	AppNameImageWarmup = "forgeai-image-warmup"
	ManagedBy          = "forgeai"

	ComponentTraining    = "training"
	ComponentImageWarmup = "image-warmup"
)

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// fnv1a mirrors the Python fnv1a package: 64-bit FNV-1a over unicode code points,
// rendered as lowercase hex without leading zeros.
func fnv1a(s string) string {
	h := fnvOffset64
	for _, r := range s {
		h ^= uint64(r)
		h *= fnvPrime64
	}
	return strconv.FormatUint(h, 16)
}

// UserHash returns the 12 char user hash used in object names.
func UserHash(namespace, userID string) string {
	if userID == "public" || userID == "pod" {
		return userID
	}
	if namespace != "pod" {
		userID = namespace + "-" + userID
	}
	h := fnv1a(userID)
	if len(h) < 16 {
		h = strings.Repeat("0", 16-len(h)) + h
	}
	return h[:12]
}

// JobName is the TrainingJob (and TrainJob) name: p-{userHash}-{jobID}.
func JobName(namespace, userID, jobID string) string {
	return strings.ToLower(fmt.Sprintf("p-%s-%s", UserHash(namespace, userID), jobID))
}

// VolumeClaimName is the PVC name of a user volume.
func VolumeClaimName(namespace, userID, volumeID string) string {
	return fmt.Sprintf("p-%s-%s", UserHash(namespace, userID), volumeID)
}

// NetworkPolicyName is the per-user egress policy name.
func NetworkPolicyName(namespace, userID string) string {
	return JobName(namespace, userID, "default")
}

func RuntimeName(job string) string { return job + "-runtime" }

// ImageWarmupName is the warmup DaemonSet name of an image.
func ImageWarmupName(image string) string {
	return "forgeai-warmup-" + ImageWarmupID(image)
}

// ImageWarmupID is a stable short hash of an image reference.
func ImageWarmupID(image string) string {
	h := fnv1a(image)
	if len(h) < 16 {
		h = strings.Repeat("0", 16-len(h)) + h
	}
	return h
}
