// Package platform loads the platform config the operator reads: namespace
// prefix, registry, instance type sizing, default quotas and network settings.
// The config is a ConfigMap written by the platform that runs the operator;
// keys the operator does not use are ignored.
package platform

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	// ConfigMapName holds the platform config under key ConfigKey.
	ConfigMapName = "workspace-platform"
	ConfigKey     = "config.yaml"
	// DefaultNamespace is where the operator and its config live.
	DefaultNamespace = "project-system"
)

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

// Gateway publishes services through a Gateway API Gateway.
type Gateway struct {
	// Name and Namespace of the Gateway the routes attach to. Its listeners
	// must admit routes from the project namespaces.
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// SectionName is the listener the routes attach to; empty means any.
	SectionName string `json:"sectionName,omitempty"`
	// Scheme and Port are the listener as seen from outside, for the URLs
	// shown to users. Scheme defaults to https.
	Scheme string `json:"scheme,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// Enabled reports whether a gateway is configured.
func (g Gateway) Enabled() bool { return g.Name != "" }

// URL returns the external URL of host, omitting the scheme's default port.
func (g Gateway) URL(host string) string {
	u := g.Scheme + "://" + host
	if g.Port != 0 && g.Port != defaultPort(g.Scheme) {
		u += fmt.Sprintf(":%d", g.Port)
	}
	return u
}

func defaultPort(scheme string) int {
	if scheme == "http" {
		return 80
	}
	return 443
}

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
	// Headroom is CPU and memory allowed outside GPU jobs (helper pods),
	// on top of the GPU-derived quota.
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
	// SandboxNamespace runs the agent-sandbox controller, which reaches the
	// pods of a project and their sandboxes.
	SandboxNamespace string `json:"sandboxNamespace,omitempty"`
}

// Config is the platform config the operator reads.
type Config struct {
	// NamespacePrefix is prepended to project names.
	NamespacePrefix string `json:"namespacePrefix,omitempty"`
	// MembersNamespace holds member ServiceAccounts and tokens, out of reach
	// of project members.
	MembersNamespace string `json:"membersNamespace,omitempty"`

	Ingress  Ingress  `json:"ingress"`
	Gateway  Gateway  `json:"gateway,omitzero"`
	Registry Registry `json:"registry"`

	InstanceTypes InstanceTypes `json:"instanceTypes"`
	DefaultQuota  Quota         `json:"defaultQuota"`
	Network       Network       `json:"network"`
}

func defaultQuantity(q *resource.Quantity, v string) {
	if q.IsZero() {
		*q = resource.MustParse(v)
	}
}

// Parse parses YAML and applies defaults.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse platform config: %w", err)
	}
	if c.NamespacePrefix == "" {
		c.NamespacePrefix = "project-"
	}
	if c.MembersNamespace == "" {
		c.MembersNamespace = "project-members"
	}
	if c.Ingress.Port == 0 {
		c.Ingress.Port = 443
	}
	if c.Gateway.Scheme == "" {
		c.Gateway.Scheme = "https"
	}
	if c.Registry.AdminSecret == "" {
		c.Registry.AdminSecret = "project-registry-admin"
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
	if c.Network.SandboxNamespace == "" {
		c.Network.SandboxNamespace = "agent-sandbox-system"
	}
	return c, nil
}

// Namespace returns the platform namespace from PROJECT_NAMESPACE or the default.
func Namespace() string {
	if ns := os.Getenv("PROJECT_NAMESPACE"); ns != "" {
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

// Invalidate drops the cached config, so that the next Get reads the
// ConfigMap again. Controllers call it when the ConfigMap changes, so that a
// change takes effect at once instead of after the cache expires.
func (l *Loader) Invalidate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg = nil
}
