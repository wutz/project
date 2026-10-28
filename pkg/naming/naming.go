// Package naming holds object names, label keys and annotations set by the
// operator. Other platforms that read them import this package.
package naming

import (
	"regexp"
	"strings"
)

// Labels set by the operator.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedBy is the LabelManagedBy value of every object the operator creates.
	ManagedBy = "project"
	// LabelProject is set on project namespaces and every object the operator
	// creates for a project (value = Project name).
	LabelProject = "project.wutz.dev/project"
	// LabelMember is set on member ServiceAccounts and token Secrets.
	LabelMember = "project.wutz.dev/member"
	// LabelGPUClass is set on generated InstanceTypes and ResourceFlavors.
	LabelGPUClass = "project.wutz.dev/gpu-class"
	// Node labels from NVIDIA GPU feature discovery.
	LabelGPUProduct = "nvidia.com/gpu.product"
	LabelGPUMemory  = "nvidia.com/gpu.memory"
)

// Fixed names inside a project namespace.
const (
	LocalQueueName     = "default"
	RegistrySecretName = "project-registry"
	NetworkPolicyName  = "project-isolation"
	ResourceQuotaName  = "project-quota"
	LimitRangeName     = "project-defaults"
)

// ClusterRolePrefix + role (edit, view) is bound per member in the project
// namespace (see config/rbac/project_roles.yaml).
const ClusterRolePrefix = "project-"

// Namespace is the namespace of a project.
func Namespace(prefix, project string) string { return prefix + project }

// ClusterQueue is the Kueue ClusterQueue of a project.
func ClusterQueue(namespace string) string { return namespace }

// ProjectClusterRole is the per-project ClusterRole (and binding) granting
// members read access to the Project, its ClusterQueue and cluster-wide
// catalogues.
func ProjectClusterRole(project string) string { return "project:project:" + project }

// MemberServiceAccount is the ServiceAccount of a member in the members
// namespace. Project and member names are DNS labels, so the dot keeps
// names unique.
func MemberServiceAccount(project, member string) string { return project + "." + member }

// MemberTokenSecret is the long-lived token Secret of a member.
func MemberTokenSecret(project, member string) string { return project + "." + member + ".token" }

// MemberRoleBinding binds a member to its role in the project namespace.
func MemberRoleBinding(member string) string { return "project-member-" + member }

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
func ResourceFlavor(class string) string { return "project-" + class }

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
