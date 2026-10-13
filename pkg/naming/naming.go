// Package naming holds object names, label keys and annotations shared by the
// operator and the GUI.
package naming

import (
	"regexp"
	"strings"
)

// Labels and annotations.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelComponent = "app.kubernetes.io/component"
	ManagedBy      = "forgeai"

	// LabelProject is set on project namespaces and every object forgeai
	// creates for a project (value = Project name).
	LabelProject = "forgeai.scclabs.dev/project"
	// LabelMember is set on member ServiceAccounts and token Secrets.
	LabelMember = "forgeai.scclabs.dev/member"

	// LabelTrash marks a volume (PVC) in the trash.
	LabelTrash = "forgeai.scclabs.dev/trash"
	// AnnotationTrashedAt is when the volume was moved to the trash (RFC3339).
	AnnotationTrashedAt = "forgeai.scclabs.dev/trashed-at"
	// AnnotationExpireAt is when a trashed volume is purged (RFC3339).
	AnnotationExpireAt = "forgeai.scclabs.dev/expire-at"
	// LabelVolumeType is the platform storage type of a volume.
	LabelVolumeType = "forgeai.scclabs.dev/volume-type"

	// AnnotationLastActive is a client heartbeat on a FileServer (RFC3339).
	AnnotationLastActive = "forgeai.scclabs.dev/last-active"

	// LabelTemplate marks a TrainingRuntime as a user job template.
	LabelTemplate = "forgeai.scclabs.dev/template"
	// AnnotationDescription describes a template.
	AnnotationDescription = "forgeai.scclabs.dev/description"
	// LabelInstanceType is the InstanceType of a TrainJob.
	LabelInstanceType = "forgeai.scclabs.dev/instance-type"
	// LabelJobGroup groups a TrainJob with its retries (value = first job name).
	LabelJobGroup = "forgeai.scclabs.dev/job-group"
	// AnnotationMaxRetries is how often a failed TrainJob is resubmitted.
	AnnotationMaxRetries = "forgeai.scclabs.dev/max-retries"
	// AnnotationRetryOn is Infra (default) or Any.
	AnnotationRetryOn = "forgeai.scclabs.dev/retry-on"
	// AnnotationRetry is the attempt number of a resubmitted TrainJob.
	AnnotationRetry = "forgeai.scclabs.dev/retry"
	// AnnotationFailureReason is the failure classification of a TrainJob.
	AnnotationFailureReason = "forgeai.scclabs.dev/failure-reason"
	// AnnotationFailureMessage details the failure.
	AnnotationFailureMessage = "forgeai.scclabs.dev/failure-message"
	// AnnotationRetriedAs is the name of the TrainJob that retried this one.
	AnnotationRetriedAs = "forgeai.scclabs.dev/retried-as"

	// LabelGPUClass is set on generated InstanceTypes and ResourceFlavors.
	LabelGPUClass = "forgeai.scclabs.dev/gpu-class"

	// Node labels from NVIDIA GPU feature discovery.
	LabelGPUProduct = "nvidia.com/gpu.product"
	LabelGPUCount   = "nvidia.com/gpu.count"
	LabelGPUMemory  = "nvidia.com/gpu.memory"

	ComponentFileServer = "fileserver"
)

// Fixed names inside a project namespace.
const (
	LocalQueueName       = "default"
	RegistrySecretName   = "forgeai-registry"
	FileServerName       = "default"
	FileServerSecretName = "forgeai-fileserver"
	NetworkPolicyName    = "forgeai-isolation"
	ResourceQuotaName    = "forgeai-quota"
	LimitRangeName       = "forgeai-defaults"
)

// ClusterRolePrefix + role (edit, view) is bound per member in the project
// namespace (see config/rbac/project_roles.yaml).
const ClusterRolePrefix = "forgeai-project-"

// Namespace is the namespace of a project.
func Namespace(prefix, project string) string { return prefix + project }

// ClusterQueue is the Kueue ClusterQueue of a project.
func ClusterQueue(namespace string) string { return namespace }

// ProjectClusterRole is the per-project ClusterRole (and binding) granting
// members read access to the Project, its ClusterQueue and cluster-wide
// catalogues.
func ProjectClusterRole(project string) string { return "forgeai:project:" + project }

// MemberServiceAccount is the ServiceAccount of a member in the members
// namespace. Project and member names are DNS labels, so the dot keeps
// names unique.
func MemberServiceAccount(project, member string) string { return project + "." + member }

// MemberTokenSecret is the long-lived token Secret of a member.
func MemberTokenSecret(project, member string) string { return project + "." + member + ".token" }

// MemberRoleBinding binds a member to its role in the project namespace.
func MemberRoleBinding(member string) string { return "forgeai-member-" + member }

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// GPUClass normalises a nvidia.com/gpu.product value (NVIDIA-GeForce-RTX-5090)
// to a short DNS label (rtx-5090).
func GPUClass(product string) string {
	s := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(product), "-"), "-")
	for _, p := range []string{"nvidia-", "geforce-", "tesla-"} {
		s = strings.TrimPrefix(s, p)
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// InstanceTypeName is the InstanceType of count GPUs of a class, e.g. rtx-5090.x2.
func InstanceTypeName(class string, count int32) string {
	return class + ".x" + itoa(count)
}

// ResourceFlavor is the Kueue ResourceFlavor of a GPU class.
func ResourceFlavor(class string) string { return "forgeai-" + class }

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	var b [11]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Keys of unstructured object maps.
const (
	KeyAPIVersion = "apiVersion"
	KeyKind       = "kind"
	KeyMetadata   = "metadata"
	KeyName       = "name"
	KeyNamespace  = "namespace"
	KeyLabels     = "labels"
	KeySpec       = "spec"
	KeyStatus     = "status"
)

// LiteralTrue is the string form of a boolean true in labels and annotations.
const LiteralTrue = "true"
