package sandbox

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/wutz/forgeai/api/v1alpha1"
	"github.com/wutz/forgeai/pkg/instancetype"
	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
)

// MaxNameLength keeps the name usable as a Service name (the controller
// creates a Service named after the sandbox).
const MaxNameLength = 63

const (
	// ContainerName is the sandbox container forgeai renders.
	ContainerName = "sandbox"
	// volumeShm and volumePrefix name the /dev/shm emptyDir and the volumes
	// mounted from project PVCs.
	volumeShm    = "dshm"
	volumePrefix = "vol-"
	// AnnotationTTL stops a sandbox after this long (form field "Auto-stop").
	AnnotationTTL = "forgeai.wutz.dev/ttl"
	// DefaultMemory and DefaultCPU size a sandbox without an instance type.
	DefaultCPU    = "1"
	DefaultMemory = "2Gi"
)

// EnvVar is a name/value pair.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// VolumeMount mounts a project volume (PVC) into the sandbox.
type VolumeMount struct {
	Volume    string `json:"volume"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath,omitempty"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

// Form is a sandbox as entered in the GUI.
type Form struct {
	Name string `json:"name"`
	// Image runs the agent; the platform default is used when empty.
	Image string `json:"image"`
	// Command is a bash script run instead of the image entrypoint.
	Command string `json:"command,omitempty"`
	// Env is set on the container.
	Env []EnvVar `json:"env,omitempty"`
	// InstanceType is optional: it fixes CPU, memory and GPUs and the node
	// placement; without it the sandbox gets DefaultCPU/DefaultMemory.
	InstanceType string `json:"instanceType,omitempty"`
	// RuntimeClass isolates the sandbox (gvisor, kata); empty uses the node runtime.
	RuntimeClass string `json:"runtimeClass,omitempty"`
	// Service exposes the sandbox under a stable DNS name.
	Service bool `json:"service,omitempty"`
	// Volumes mounts project volumes.
	Volumes []VolumeMount `json:"volumes,omitempty"`
	// TTL is how long the sandbox lives before the controller stops it
	// (empty or 0: it runs until suspended or deleted).
	TTL metav1.Duration `json:"ttl,omitzero"`
}

var nameRE = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// nanos is a duration in nanoseconds, the shape of the GUI's duration model.
type nanos int64

// UnmarshalJSON accepts both encodings of the auto-stop duration that reach
// this struct: a Go duration string ("1h0m0s"), which is what metav1.Duration
// marshals to, and the {"Duration": <nanoseconds>} object the generated Wails
// model sends from the GUI form.
func (f *Form) UnmarshalJSON(data []byte) error {
	type form Form // without methods, so this decoder does not recurse
	var decoded form
	if err := json.Unmarshal(data, &decoded); err == nil {
		*f = Form(decoded)
		return nil
	}
	// The duration arrives as an object: decode every field through form and
	// take ttl from the object's Duration field.
	var model struct {
		*form
		TTL *struct {
			Duration nanos `json:"Duration"`
		} `json:"ttl"`
	}
	model.form = &decoded
	if err := json.Unmarshal(data, &model); err != nil {
		return err
	}
	if model.TTL != nil {
		decoded.TTL = metav1.Duration{Duration: time.Duration(model.TTL.Duration)}
	}
	*f = Form(decoded)
	return nil
}

// Validate checks a form; it does not look up the instance type or volumes.
func (f *Form) Validate() error {
	if !nameRE.MatchString(f.Name) || len(f.Name) > MaxNameLength {
		return fmt.Errorf("invalid name %q: lowercase letters, digits and '-', starting with a letter, at most %d characters",
			f.Name, MaxNameLength)
	}
	if strings.TrimSpace(f.Image) == "" {
		return fmt.Errorf("image is required")
	}
	paths := map[string]bool{}
	for _, m := range f.Volumes {
		if m.Volume == "" || !strings.HasPrefix(m.MountPath, "/") || paths[m.MountPath] {
			return fmt.Errorf("volume mounts need a volume and a unique absolute mount path")
		}
		paths[m.MountPath] = true
	}
	if f.TTL.Duration < 0 {
		return fmt.Errorf("ttl must not be negative")
	}
	return nil
}

// Context is what a sandbox needs beyond the form.
type Context struct {
	Namespace    string
	Project      string
	InstanceType *v1.InstanceType
	Platform     *platform.Config
	// RegistrySecret is added to imagePullSecrets when set.
	RegistrySecret string
	// Now is the creation time (tests pin it); zero uses time.Now.
	Now time.Time
}

func command(script string) []string {
	if strings.TrimSpace(script) == "" {
		return nil
	}
	return []string{"/bin/bash", "-c", script}
}

func env(in []EnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(in))
	for _, e := range in {
		out = append(out, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	return out
}

func resources(it *v1.InstanceType) corev1.ResourceRequirements {
	var rl corev1.ResourceList
	if it == nil {
		rl = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultCPU),
			corev1.ResourceMemory: resource.MustParse(DefaultMemory),
		}
	} else {
		rl = corev1.ResourceList{
			corev1.ResourceCPU:       it.Spec.CPU.DeepCopy(),
			corev1.ResourceMemory:    it.Spec.Memory.DeepCopy(),
			instancetype.ResourceGPU: *resource.NewQuantity(int64(it.Spec.GPU.Count), resource.DecimalSI),
		}
	}
	return corev1.ResourceRequirements{Requests: rl, Limits: rl.DeepCopy()}
}

func volumes(f *Form, shm resource.Quantity) ([]corev1.Volume, []corev1.VolumeMount) {
	vols := []corev1.Volume{{Name: volumeShm, VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: &shm},
	}}}
	mounts := make([]corev1.VolumeMount, 1, 1+len(f.Volumes))
	mounts[0] = corev1.VolumeMount{Name: volumeShm, MountPath: "/dev/shm"}
	seen := map[string]bool{}
	for _, m := range f.Volumes {
		name := volumePrefix + m.Volume
		if !seen[m.Volume] {
			seen[m.Volume] = true
			vols = append(vols, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: m.Volume},
			}})
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name: name, MountPath: m.MountPath, SubPath: m.SubPath, ReadOnly: m.ReadOnly,
		})
	}
	return vols, mounts
}

// Render builds the Sandbox of a form. Placement, resources, volumes, the
// isolation runtime class and the registry pull secret come from the platform
// settings and the instance type; the agent itself from the form.
func Render(f *Form, c Context) (*unstructured.Unstructured, error) {
	cfg := c.Platform
	if f.Image == "" && cfg != nil {
		f.Image = cfg.Sandbox.DefaultImage
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	it := c.InstanceType
	if it != nil && it.Name != f.InstanceType {
		return nil, fmt.Errorf("instance type %q not found", f.InstanceType)
	}
	runtimeClass := f.RuntimeClass
	if runtimeClass == "" && cfg != nil {
		runtimeClass = cfg.Sandbox.DefaultRuntimeClass
	}
	if runtimeClass != "" && cfg != nil && !cfg.Sandbox.HasRuntimeClass(runtimeClass) {
		return nil, fmt.Errorf("runtime class %q is not offered by the platform", runtimeClass)
	}
	shm := resource.MustParse("2Gi")
	if cfg != nil && !cfg.Sandbox.ShmSize.IsZero() {
		shm = cfg.Sandbox.ShmSize
	}
	vols, mounts := volumes(f, shm)
	pod := corev1.PodSpec{
		// The sandbox controller recreates a failed pod; a sandbox is a
		// long-running environment, not a batch job.
		RestartPolicy: corev1.RestartPolicyAlways,
		Containers: []corev1.Container{{
			Name:         ContainerName,
			Image:        f.Image,
			Command:      command(f.Command),
			Env:          env(f.Env),
			Resources:    resources(it),
			VolumeMounts: mounts,
		}},
		Volumes: vols,
	}
	if it != nil {
		pod.NodeSelector = it.Spec.NodeSelector
		pod.Tolerations = []corev1.Toleration{{
			Key: string(instancetype.ResourceGPU), Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
		}}
	}
	if runtimeClass != "" {
		pod.RuntimeClassName = &runtimeClass
	}
	if c.RegistrySecret != "" {
		pod.ImagePullSecrets = []corev1.LocalObjectReference{{Name: c.RegistrySecret}}
	}
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	labels := map[string]string{
		naming.LabelManagedBy: naming.ManagedBy,
		naming.LabelComponent: naming.ComponentSandbox,
	}
	if c.Project != "" {
		labels[naming.LabelProject] = c.Project
	}
	if f.InstanceType != "" {
		labels[naming.LabelInstanceType] = f.InstanceType
	}
	sb := &Sandbox{
		Spec: SandboxSpec{
			OperatingMode: OperatingRunning,
			Blueprint:     Blueprint{PodTemplate: PodTemplate{Spec: pod}},
		},
	}
	if f.Service {
		sb.Spec.Service = new(true)
	}
	if f.TTL.Duration > 0 {
		at := metav1.NewTime(now.Add(f.TTL.Duration))
		sb.Spec.ShutdownTime = &at
		sb.Spec.ShutdownPolicy = new(ShutdownRetain)
	}
	sb.Name, sb.Namespace = f.Name, c.Namespace
	sb.Labels = labels
	sb.Annotations = map[string]string{}
	if f.TTL.Duration > 0 {
		sb.Annotations[AnnotationTTL] = f.TTL.Duration.String()
	}
	return ToUnstructured(sb)
}

// ToUnstructured converts a rendered sandbox into the create payload, dropping
// the server-owned status and timestamps.
func ToUnstructured(sb *Sandbox) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sb)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(SandboxGVK)
	unstructured.RemoveNestedField(u.Object, naming.KeyMetadata, "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, naming.KeyStatus)
	return u, nil
}

// FormOf rebuilds a form from a sandbox, for "New from this sandbox" in the GUI.
func FormOf(sb *Sandbox) *Form {
	f := &Form{
		Name:         sb.Name,
		RuntimeClass: ptrString(sb.Spec.PodTemplate.Spec.RuntimeClassName),
		Service:      sb.Spec.Service != nil && *sb.Spec.Service,
		InstanceType: sb.Labels[naming.LabelInstanceType],
	}
	for _, ctr := range sb.Spec.PodTemplate.Spec.Containers {
		if ctr.Name != ContainerName {
			continue
		}
		f.Image = ctr.Image
		if len(ctr.Command) >= 3 && ctr.Command[0] == "/bin/bash" && ctr.Command[1] == "-c" {
			f.Command = strings.Join(ctr.Command[2:], " ")
		} else {
			f.Command = strings.Join(ctr.Command, " ")
		}
		for _, e := range ctr.Env {
			f.Env = append(f.Env, EnvVar{Name: e.Name, Value: e.Value})
		}
		for _, m := range ctr.VolumeMounts {
			if m.Name == volumeShm {
				continue
			}
			f.Volumes = append(f.Volumes, VolumeMount{
				Volume: strings.TrimPrefix(m.Name, volumePrefix), MountPath: m.MountPath,
				SubPath: m.SubPath, ReadOnly: m.ReadOnly,
			})
		}
	}
	if sb.Spec.ShutdownTime != nil && !sb.Spec.ShutdownTime.IsZero() {
		f.TTL = metav1.Duration{Duration: time.Until(sb.Spec.ShutdownTime.Time).Round(time.Minute)}
		if f.TTL.Duration < 0 {
			f.TTL.Duration = 0
		}
	}
	return f
}

func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// IsSandboxPaused reports whether the sandbox is administratively suspended.
func IsSandboxPaused(sb *Sandbox) bool {
	if sb.Spec.OperatingMode == OperatingSuspended {
		return true
	}
	for _, c := range sb.Status.Conditions {
		if c.Type == ConditionSuspended {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

// CPUAndMemory are the total CPU and memory requests of a sandbox pod, for
// the resource quota in the GUI.
func CPUAndMemory(sb *Sandbox) (cpu, mem resource.Quantity) {
	for _, c := range sb.Spec.PodTemplate.Spec.Containers {
		if c.Resources.Requests != nil {
			cpu.Add(*c.Resources.Requests.Cpu())
			mem.Add(*c.Resources.Requests.Memory())
		}
	}
	return cpu, mem
}

// GPU is the GPU count requested by a sandbox.
func GPU(sb *Sandbox) int64 {
	var n int64
	for _, c := range sb.Spec.PodTemplate.Spec.Containers {
		if q, ok := c.Resources.Requests[instancetype.ResourceGPU]; ok {
			n += q.Value()
		}
	}
	return n
}
