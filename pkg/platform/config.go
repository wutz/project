// Package platform loads the forgeai platform config (resource models, domains,
// access settings) shared by the operator, compat API and GUI.
package platform

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
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

// Resource is a resource model (same shape as scc-k8s-v2 settings.resources).
type Resource struct {
	ID  string `json:"id"`
	GPU *struct {
		Count int    `json:"count"`
		Model string `json:"model,omitempty"`
	} `json:"gpu,omitempty"`
	CPU struct {
		Cores float64 `json:"cores"`
		Model string  `json:"model,omitempty"`
	} `json:"cpu"`
	Memory struct {
		SizeGB float64 `json:"sizeGB"`
	} `json:"memory"`
	Labels map[string]string `json:"labels,omitempty"`
}

// GPUCount returns the GPU count, 0 for CPU-only models.
func (r Resource) GPUCount() int {
	if r.GPU == nil {
		return 0
	}
	return r.GPU.Count
}

// RDMAProfile describes how multi-node jobs get RDMA NICs on a GPU type.
type RDMAProfile struct {
	// Networks is the Multus k8s.v1.cni.cncf.io/networks list.
	Networks []NetworkRef `json:"networks"`
	// Resources are extra container resources (e.g. rdma/hca: 1).
	Resources map[string]string `json:"resources,omitempty"`
}

type NetworkRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type SSHConfig struct {
	// PiperService is namespace/name of the sshpiper LoadBalancer service.
	PiperService string `json:"piperService"`
	// Domains maps LB IP to user-facing domains.
	Domains map[string][]string `json:"domains,omitempty"`
}

type Config struct {
	ReleaseVersion string `json:"releaseVersion"`
	// RootDomain and ExtraDomains are used for per-worker Jupyter ingress hosts.
	RootDomain   string    `json:"rootDomain"`
	ExtraDomains []string  `json:"extraDomains,omitempty"`
	IngressClass string    `json:"ingressClass,omitempty"`
	SSH          SSHConfig `json:"ssh"`

	// DefaultQueue is the Kueue LocalQueue used when a job sets none.
	DefaultQueue   string `json:"defaultQueue"`
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// BaseToolsHostPath is mounted read-only at /base (ssh/jupyter entrypoint).
	BaseToolsHostPath string `json:"baseToolsHostPath,omitempty"`
	EphemeralStorage  string `json:"ephemeralStorage,omitempty"`
	// ExtraResources are added to every worker (e.g. nextflow.io/fuse: 1).
	ExtraResources map[string]string `json:"extraResources,omitempty"`

	// MultiNode settings apply when workers > 1.
	MultiNode struct {
		// FullNodeGPUs forces this GPU count per worker (0 = keep model count).
		FullNodeGPUs int               `json:"fullNodeGPUs,omitempty"`
		Env          map[string]string `json:"env,omitempty"`
		// RDMA profiles keyed by scclabs.dev/gpu label value; "*" is the fallback.
		RDMA map[string]RDMAProfile `json:"rdma,omitempty"`
		// LeafTopologyKey enables soft pod affinity to keep workers under one leaf switch.
		LeafTopologyKey string `json:"leafTopologyKey,omitempty"`
		// PodSetTopology sets a Kueue TAS annotation, e.g.
		// {"kueue.x-k8s.io/podset-preferred-topology": "kubernetes.io/hostname"}.
		PodSetTopology map[string]string `json:"podSetTopology,omitempty"`
	} `json:"multiNode"`

	// UserNetworkPolicy creates the scc-k8s-v2 style per-user egress policy.
	UserNetworkPolicy bool `json:"userNetworkPolicy"`

	Resources []Resource `json:"resources"`
}

// Resource looks up a resource model.
func (c *Config) Resource(id string) (Resource, error) {
	for _, r := range c.Resources {
		if r.ID == id {
			return r, nil
		}
	}
	return Resource{}, fmt.Errorf("resource %q not found", id)
}

// Domains returns the root domain followed by extra domains.
func (c *Config) Domains() []string {
	out := []string{}
	for _, d := range append([]string{c.RootDomain}, c.ExtraDomains...) {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// Parse parses YAML and applies defaults.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse platform config: %w", err)
	}
	if c.DefaultQueue == "" {
		c.DefaultQueue = "forgeai-queue"
	}
	if c.ServiceAccount == "" {
		c.ServiceAccount = "default"
	}
	if c.EphemeralStorage == "" {
		c.EphemeralStorage = "50Gi"
	}
	if c.ReleaseVersion == "" {
		c.ReleaseVersion = "dev"
	}
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
