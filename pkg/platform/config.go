// Package platform loads the forgeai platform config shared by the operator
// and the GUI: namespace prefix, storage types, registry, instance type
// sizing, default quotas and service settings.
package platform

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	// ConfigMapName holds the platform config under key ConfigKey.
	ConfigMapName = "forgeai-platform"
	ConfigKey     = "config.yaml"
	// DefaultNamespace is where the operator and its config live.
	DefaultNamespace = "forgeai-system"
)

// StorageType maps a volume type to a StorageClass.
type StorageType struct {
	StorageClass string `json:"storageClass"`
	// AccessMode defaults to ReadWriteMany.
	AccessMode corev1.PersistentVolumeAccessMode `json:"accessMode,omitempty"`
	// Description is shown in the GUI.
	Description string `json:"description,omitempty"`
}

// Ingress is how user-facing services are exposed.
type Ingress struct {
	ClassName string `json:"className,omitempty"`
	// Domain is the wildcard DNS suffix, e.g. dev1.example.com.
	Domain string `json:"domain,omitempty"`
	// Port is the external HTTPS port (443 is omitted from URLs).
	Port int `json:"port,omitempty"`
	// TLSSecret is an optional certificate Secret in each project namespace;
	// empty uses the ingress controller default certificate.
	TLSSecret string `json:"tlsSecret,omitempty"`
}

// URL returns the external URL of host <sub>.<domain>.
func (i Ingress) URL(sub string) string {
	u := "https://" + i.Host(sub)
	if i.Port != 0 && i.Port != 443 {
		u += fmt.Sprintf(":%d", i.Port)
	}
	return u
}

// Host returns <sub>.<domain>.
func (i Ingress) Host(sub string) string { return sub + "." + i.Domain }

// Registry is the Harbor instance holding project images.
type Registry struct {
	// Domain is host[:port] used for pushes and pulls.
	Domain string `json:"domain,omitempty"`
	// URL of the Harbor API; defaults to https://<domain>.
	URL string `json:"url,omitempty"`
	// Insecure skips TLS verification.
	Insecure bool `json:"insecure,omitempty"`
	// AdminSecret in the platform namespace holds username and password.
	AdminSecret string `json:"adminSecret,omitempty"`
}

// Enabled reports whether a registry is configured.
func (r Registry) Enabled() bool { return r.Domain != "" }

// APIURL is the Harbor base URL.
func (r Registry) APIURL() string {
	if r.URL != "" {
		return r.URL
	}
	return "https://" + r.Domain
}

// InstanceTypes controls how GPU nodes are sliced.
type InstanceTypes struct {
	// Reserved is kept back on every node for the system before slicing.
	Reserved struct {
		CPU    resource.Quantity `json:"cpu"`
		Memory resource.Quantity `json:"memory"`
	} `json:"reserved"`
	// Sizes are the GPU counts offered (capped at the GPUs per node).
	Sizes []int32 `json:"sizes,omitempty"`
}

// Quota is the default project quota.
type Quota struct {
	// GPUs per GPU class; "*" applies to classes not listed.
	GPUs map[string]int32 `json:"gpus,omitempty"`
	// Storage is the total size of all volumes.
	Storage resource.Quantity `json:"storage"`
	Volumes int32             `json:"volumes,omitempty"`
	Pods    int32             `json:"pods,omitempty"`
	// Headroom is CPU and memory allowed outside GPU jobs (file server,
	// helper pods), on top of the GPU-derived quota.
	Headroom struct {
		CPU    resource.Quantity `json:"cpu"`
		Memory resource.Quantity `json:"memory"`
	} `json:"headroom"`
}

// GPUCount returns the default GPU quota of a class.
func (q Quota) GPUCount(class string) int32 {
	if n, ok := q.GPUs[class]; ok {
		return n
	}
	return q.GPUs["*"]
}

// Network configures project isolation.
type Network struct {
	// SystemNamespaces may always reach project pods (ingress controller,
	// monitoring).
	SystemNamespaces []string `json:"systemNamespaces,omitempty"`
	// DNSNamespace runs the cluster DNS, allowed when denyInternet is set.
	DNSNamespace string `json:"dnsNamespace,omitempty"`
}

// FileServer configures the on-demand file transfer service.
type FileServer struct {
	Image       string          `json:"image,omitempty"`
	IdleTimeout metav1.Duration `json:"idleTimeout,omitzero"`
	// HostPrefix builds the host files-<namespace>.<ingress domain>.
	HostPrefix string `json:"hostPrefix,omitempty"`
}

// Training configures job rendering and failure handling.
type Training struct {
	// DefaultRuntime is the ClusterTrainingRuntime used by jobs without a template.
	DefaultRuntime string `json:"defaultRuntime,omitempty"`
	// ShmSize of the /dev/shm emptyDir.
	ShmSize resource.Quantity `json:"shmSize"`
	// StuckTimeout fails a job whose pods cannot start (image pull errors,
	// crash loops) for this long.
	StuckTimeout metav1.Duration `json:"stuckTimeout,omitzero"`
	// MaxRetries is the default retry count of jobs that set none.
	MaxRetries int32 `json:"maxRetries,omitempty"`
}

// Sandbox configures the agent sandboxes (agents.x-k8s.io Sandboxes): the
// images offered, the isolation runtime classes and the idle policy.
type Sandbox struct {
	// DefaultImage runs a sandbox whose form names none.
	DefaultImage string `json:"defaultImage,omitempty"`
	// Images are the images offered in the GUI (others can be typed in).
	Images []string `json:"images,omitempty"`
	// RuntimeClasses are the isolation runtime classes offered (gvisor,
	// kata); DefaultRuntimeClass is preselected. The RuntimeClass objects
	// must be installed by the platform.
	RuntimeClasses      []string `json:"runtimeClasses,omitempty"`
	DefaultRuntimeClass string   `json:"defaultRuntimeClass,omitempty"`
	// ShmSize is the /dev/shm emptyDir of a sandbox.
	ShmSize resource.Quantity `json:"shmSize"`
	// IdleTimeout suspends a running sandbox that nothing used for this
	// long; 0 disables it.
	IdleTimeout metav1.Duration `json:"idleTimeout,omitzero"`
	// Namespace runs the agent-sandbox controller; project network policies
	// let it reach the sandbox pods.
	Namespace string `json:"namespace,omitempty"`
}

// Inference configures the model servers of InferenceServices and the llm-d
// routers in front of them.
type Inference struct {
	// Image is the vLLM server image, used by services without an image.
	Image string `json:"image,omitempty"`
	// RouterImage is the llm-d router Endpoint Picker image, with its tag.
	RouterImage string `json:"routerImage,omitempty"`
	// ShmSize is the /dev/shm emptyDir of a model server: tensor parallel
	// workers exchange data through shared memory.
	ShmSize resource.Quantity `json:"shmSize"`
	// HFEndpoint is the Hugging Face endpoint the model servers download
	// weights from (HF_ENDPOINT). Clusters without access to huggingface.co set
	// a mirror here, such as https://hf-mirror.com.
	HFEndpoint string `json:"hfEndpoint,omitempty"`
	// Cache keeps the weights of each InferenceService on a PersistentVolume-
	// Claim, so that a restart loads them again instead of downloading them.
	// Empty uses an emptyDir, and every start downloads the weights.
	Cache InferenceCache `json:"cache,omitempty"`
	// StartupTimeout is how long a model server may take to become healthy: a
	// cold start downloads the weights (tens of GB, over a mirror at a few MB/s)
	// before it loads them into the GPUs. The pod is killed when it expires.
	StartupTimeout metav1.Duration `json:"startupTimeout,omitzero"`
}

// InferenceCache is the volume holding the downloaded weights of a model.
type InferenceCache struct {
	// StorageClass of the claim; the default StorageClass when empty.
	StorageClass string `json:"storageClass,omitempty"`
	// Size of the claim.
	Size resource.Quantity `json:"size"`
}

// Enabled reports whether a cache was configured.
func (c InferenceCache) Enabled() bool { return c.StorageClass != "" || !c.Size.IsZero() }

// DefaultSandboxNamespace is where agent-sandbox is installed.
const DefaultSandboxNamespace = "agent-sandbox-system"

// Templates configures the public template source the GUI offers in the
// create dialogs.
type Templates struct {
	// Source is an https:// URL or a file:// directory holding index.yaml.
	// Empty uses the forgeai default source (wutz/forgeai-templates on its main branch).
	Source string `json:"source,omitempty"`
}

// NamespaceOrDefault is the agent-sandbox namespace.
func (s Sandbox) NamespaceOrDefault() string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return DefaultSandboxNamespace
}

// HasRuntimeClass reports whether a runtime class is offered (or none is
// configured, in which case anything the cluster has is accepted).
func (s Sandbox) HasRuntimeClass(name string) bool {
	if len(s.RuntimeClasses) == 0 {
		return true
	}
	return slices.Contains(s.RuntimeClasses, name)
}

type Config struct {
	// NamespacePrefix is prepended to project names.
	NamespacePrefix string `json:"namespacePrefix,omitempty"`
	// APIServer is the URL written into member kubeconfigs.
	APIServer string `json:"apiServer,omitempty"`
	// MembersNamespace holds member ServiceAccounts and tokens, out of reach
	// of project members.
	MembersNamespace string `json:"membersNamespace,omitempty"`

	Ingress  Ingress  `json:"ingress"`
	Registry Registry `json:"registry"`

	// StorageTypes maps a volume type (e.g. shared) to its StorageClass.
	StorageTypes       map[string]StorageType `json:"storageTypes,omitempty"`
	DefaultStorageType string                 `json:"defaultStorageType,omitempty"`

	InstanceTypes InstanceTypes `json:"instanceTypes"`
	DefaultQuota  Quota         `json:"defaultQuota"`
	Network       Network       `json:"network"`
	FileServer    FileServer    `json:"fileServer"`
	Training      Training      `json:"training"`
	Sandbox       Sandbox       `json:"sandbox"`
	Inference     Inference     `json:"inference"`
	Templates     Templates     `json:"templates,omitzero"`

	// TrashRetention is the default retention of deleted volumes.
	TrashRetention metav1.Duration `json:"trashRetention,omitzero"`
}

// StorageType looks up a volume type; empty selects the default.
func (c *Config) StorageType(name string) (string, StorageType, error) {
	if name == "" {
		name = c.DefaultStorageType
	}
	if t, ok := c.StorageTypes[name]; ok && t.StorageClass != "" {
		if t.AccessMode == "" {
			t.AccessMode = corev1.ReadWriteMany
		}
		return name, t, nil
	}
	return "", StorageType{}, fmt.Errorf("volume type %q is not supported", name)
}

func defaultQuantity(q *resource.Quantity, v string) {
	if q.IsZero() {
		*q = resource.MustParse(v)
	}
}

func defaultDuration(d *metav1.Duration, v time.Duration) {
	if d.Duration == 0 {
		d.Duration = v
	}
}

// Parse parses YAML and applies defaults.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse platform config: %w", err)
	}
	if c.NamespacePrefix == "" {
		c.NamespacePrefix = "forge-"
	}
	if c.MembersNamespace == "" {
		c.MembersNamespace = "forgeai-members"
	}
	if c.Ingress.Port == 0 {
		c.Ingress.Port = 443
	}
	if c.Registry.AdminSecret == "" {
		c.Registry.AdminSecret = "forgeai-harbor"
	}
	if len(c.InstanceTypes.Sizes) == 0 {
		c.InstanceTypes.Sizes = []int32{1, 2, 4, 8}
	}
	defaultQuantity(&c.InstanceTypes.Reserved.CPU, "2")
	defaultQuantity(&c.InstanceTypes.Reserved.Memory, "16Gi")
	if c.DefaultQuota.GPUs == nil {
		c.DefaultQuota.GPUs = map[string]int32{"*": 8}
	}
	defaultQuantity(&c.DefaultQuota.Storage, "1Ti")
	defaultQuantity(&c.DefaultQuota.Headroom.CPU, "4")
	defaultQuantity(&c.DefaultQuota.Headroom.Memory, "8Gi")
	if c.DefaultQuota.Volumes == 0 {
		c.DefaultQuota.Volumes = 20
	}
	if c.DefaultQuota.Pods == 0 {
		c.DefaultQuota.Pods = 100
	}
	if c.Network.DNSNamespace == "" {
		c.Network.DNSNamespace = "kube-system"
	}
	if c.FileServer.Image == "" {
		c.FileServer.Image = "docker.io/sigoden/dufs:v0.46.0"
	}
	if c.FileServer.HostPrefix == "" {
		c.FileServer.HostPrefix = "files-"
	}
	defaultDuration(&c.FileServer.IdleTimeout, 30*time.Minute)
	if c.Training.DefaultRuntime == "" {
		c.Training.DefaultRuntime = "torch-distributed"
	}
	defaultQuantity(&c.Training.ShmSize, "16Gi")
	defaultDuration(&c.Training.StuckTimeout, 15*time.Minute)
	if c.Sandbox.Namespace == "" {
		c.Sandbox.Namespace = DefaultSandboxNamespace
	}
	if c.Sandbox.DefaultImage == "" {
		c.Sandbox.DefaultImage = "registry.k8s.io/agent-sandbox/python-runtime-sandbox:v0.1.0"
	}
	if c.Sandbox.DefaultRuntimeClass == "" && len(c.Sandbox.RuntimeClasses) > 0 {
		c.Sandbox.DefaultRuntimeClass = c.Sandbox.RuntimeClasses[0]
	}
	defaultQuantity(&c.Sandbox.ShmSize, "2Gi")
	if c.Inference.Image == "" {
		c.Inference.Image = "vllm/vllm-openai:v0.32.0"
	}
	if c.Inference.RouterImage == "" {
		c.Inference.RouterImage = "ghcr.io/llm-d/llm-d-router-endpoint-picker:v1.0.0"
	}
	defaultQuantity(&c.Inference.ShmSize, "16Gi")
	defaultDuration(&c.Inference.StartupTimeout, 2*time.Hour)
	if c.Inference.Cache.Enabled() {
		defaultQuantity(&c.Inference.Cache.Size, "200Gi")
	}
	defaultDuration(&c.TrashRetention, 7*24*time.Hour)
	return c, nil
}

// Namespace returns the platform namespace from FORGEAI_NAMESPACE or the default.
func Namespace() string {
	if ns := os.Getenv("FORGEAI_NAMESPACE"); ns != "" {
		return ns
	}
	return DefaultNamespace
}

// Load reads the platform ConfigMap.
func Load(ctx context.Context, c client.Reader, namespace string) (*Config, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ConfigMapName}, cm); err != nil {
		return nil, fmt.Errorf("get platform config %s/%s: %w", namespace, ConfigMapName, err)
	}
	return Parse([]byte(cm.Data[ConfigKey]))
}

// Loader caches the platform config for a short time.
type Loader struct {
	Reader    client.Reader
	Namespace string

	mu   sync.Mutex
	cfg  *Config
	time time.Time
}

// Get returns the cached config, reloading it after 15 seconds.
func (l *Loader) Get(ctx context.Context) (*Config, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cfg != nil && time.Since(l.time) < 15*time.Second {
		return l.cfg, nil
	}
	cfg, err := Load(ctx, l.Reader, l.Namespace)
	if err != nil {
		return nil, err
	}
	l.cfg, l.time = cfg, time.Now()
	return cfg, nil
}
