// Package instancetype derives GPU instance types from the cluster's GPU
// nodes and computes how many of each fit now. It is a pure function of
// nodes, pods and the platform settings.
package instancetype

import (
	"cmp"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/scclabs/forgeai/api/v1alpha1"
	"github.com/scclabs/forgeai/pkg/naming"
	"github.com/scclabs/forgeai/pkg/platform"
)

// ResourceGPU is the NVIDIA device plugin resource.
const ResourceGPU corev1.ResourceName = "nvidia.com/gpu"

const (
	milliCore = 1000
	gib       = int64(1) << 30
)

// Class is a group of nodes with the same GPU product.
type Class struct {
	Name    string
	Product string
	// MemoryMiB of one GPU.
	MemoryMiB int64
	// MaxGPUs is the largest GPU count of a node in the class.
	MaxGPUs int32
	// MilliCPU and Memory (bytes) per GPU: (allocatable - reserved) / GPUs,
	// rounded down to whole cores and GiB, minimum over the nodes.
	MilliCPU int64
	Memory   int64
	Nodes    []string
}

// CPUPerGPU returns the per-GPU CPU slice as a quantity.
func (c Class) CPUPerGPU() resource.Quantity {
	return *resource.NewMilliQuantity(c.MilliCPU, resource.DecimalSI)
}

// MemoryPerGPU returns the per-GPU memory slice as a quantity.
func (c Class) MemoryPerGPU() resource.Quantity {
	return *resource.NewQuantity(c.Memory, resource.BinarySI)
}

// node is the scheduling view of a GPU node.
type node struct {
	name                     string
	class                    string
	schedulable              bool
	gpus, cpu, mem           int64 // allocatable
	usedGPU                  int64
	usedCPU, usedMem         int64 // by pods requesting GPUs
	otherCPU, otherMem       int64 // by other pods (system daemons first use the reservation)
	reservedCPU, reservedMem int64
}

func quantity(l corev1.ResourceList, name corev1.ResourceName) int64 {
	if q, ok := l[name]; ok {
		return q.Value()
	}
	return 0
}

// tolerated taints on GPU nodes; any other NoSchedule/NoExecute taint makes
// the node unusable for instances.
func schedulable(n *corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	ready := false
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			ready = c.Status == corev1.ConditionTrue
		}
	}
	if !ready {
		return false
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectPreferNoSchedule {
			continue
		}
		if t.Key == string(ResourceGPU) && t.Effect == corev1.TaintEffectNoSchedule {
			continue
		}
		return false
	}
	return true
}

// PodRequests returns the effective requests of a pod as the scheduler sees
// them: max(sum(containers)+sidecars, each init container) + overhead.
func PodRequests(p *corev1.Pod) corev1.ResourceList {
	reqs := corev1.ResourceList{}
	add := func(dst, src corev1.ResourceList) {
		for k, v := range src {
			q := dst[k]
			q.Add(v)
			dst[k] = q
		}
	}
	for _, c := range p.Spec.Containers {
		add(reqs, c.Resources.Requests)
	}
	sidecars := corev1.ResourceList{}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			add(reqs, c.Resources.Requests)
			add(sidecars, c.Resources.Requests)
			continue
		}
		need := sidecars.DeepCopy()
		add(need, c.Resources.Requests)
		for k, v := range need {
			if cur, ok := reqs[k]; !ok || v.Cmp(cur) > 0 {
				reqs[k] = v.DeepCopy()
			}
		}
	}
	add(reqs, p.Spec.Overhead)
	return reqs
}

// Compute returns the GPU classes and the instance types with availability.
func Compute(
	nodes []corev1.Node, pods []corev1.Pod, cfg platform.InstanceTypes, now metav1.Time,
) ([]Class, []v1.InstanceType) {
	resCPU := cfg.Reserved.CPU.MilliValue()
	resMem := cfg.Reserved.Memory.Value()

	byName := map[string]*node{}
	classes := map[string]*Class{}
	for i := range nodes {
		n := &nodes[i]
		product := n.Labels[naming.LabelGPUProduct]
		gpus := quantity(n.Status.Allocatable, ResourceGPU)
		if product == "" || gpus <= 0 {
			continue
		}
		cls := naming.GPUClass(product)
		cpu := n.Status.Allocatable.Cpu().MilliValue()
		mem := n.Status.Allocatable.Memory().Value()
		byName[n.Name] = &node{
			name: n.Name, class: cls, schedulable: schedulable(n), gpus: gpus, cpu: cpu, mem: mem,
			reservedCPU: resCPU, reservedMem: resMem,
		}
		sliceCPU := max(cpu-resCPU, 0) / gpus / milliCore * milliCore
		sliceMem := max(mem-resMem, 0) / gpus / gib * gib
		c := classes[cls]
		if c == nil {
			c = &Class{Name: cls, Product: product, MilliCPU: sliceCPU, Memory: sliceMem}
			c.MemoryMiB, _ = strconv.ParseInt(n.Labels[naming.LabelGPUMemory], 10, 64)
			classes[cls] = c
		}
		c.MilliCPU = min(c.MilliCPU, sliceCPU)
		c.Memory = min(c.Memory, sliceMem)
		c.MaxGPUs = max(c.MaxGPUs, int32(gpus))
		c.Nodes = append(c.Nodes, n.Name)
	}

	for i := range pods {
		p := &pods[i]
		n := byName[p.Spec.NodeName]
		if n == nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		req := PodRequests(p)
		if g := quantity(req, ResourceGPU); g > 0 {
			n.usedGPU += g
			n.usedCPU += req.Cpu().MilliValue()
			n.usedMem += req.Memory().Value()
			continue
		}
		n.otherCPU += req.Cpu().MilliValue()
		n.otherMem += req.Memory().Value()
	}

	out := make([]Class, 0, len(classes))
	for _, c := range classes {
		slices.Sort(c.Nodes)
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Class) int { return cmp.Compare(a.Name, b.Name) })

	var types []v1.InstanceType
	for _, c := range out {
		if c.MilliCPU == 0 || c.Memory == 0 {
			continue
		}
		for _, size := range cfg.Sizes {
			if size < 1 || size > c.MaxGPUs {
				continue
			}
			types = append(types, instanceType(c, size, byName, now))
		}
	}
	return out, types
}

func floorDiv(a, b int64) int64 {
	if b <= 0 || a <= 0 {
		return 0
	}
	return a / b
}

func instanceType(c Class, size int32, nodes map[string]*node, now metav1.Time) v1.InstanceType {
	n64 := int64(size)
	it := v1.InstanceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: naming.InstanceTypeName(c.Name, size),
			Labels: map[string]string{
				naming.LabelManagedBy: naming.ManagedBy,
				naming.LabelGPUClass:  c.Name,
			},
		},
		Spec: v1.InstanceTypeSpec{
			GPU:          v1.InstanceTypeGPU{Class: c.Name, Product: c.Product, Count: size, MemoryMiB: c.MemoryMiB},
			CPU:          *resource.NewMilliQuantity(c.MilliCPU*n64, resource.DecimalSI),
			Memory:       *resource.NewQuantity(c.Memory*n64, resource.BinarySI),
			NodeSelector: map[string]string{naming.LabelGPUProduct: c.Product},
		},
		Status: v1.InstanceTypeStatus{Nodes: []v1.NodeAvailability{}, UpdateTime: &now},
	}
	needCPU, needMem := c.MilliCPU*n64, c.Memory*n64
	for _, name := range c.Nodes {
		n := nodes[name]
		a := v1.NodeAvailability{Node: name}
		if n.schedulable {
			a.Total = int32(floorDiv(n.gpus, n64))
			freeCPU := n.cpu - n.reservedCPU - n.usedCPU - max(n.otherCPU-n.reservedCPU, 0)
			freeMem := n.mem - n.reservedMem - n.usedMem - max(n.otherMem-n.reservedMem, 0)
			free := min(floorDiv(n.gpus-n.usedGPU, n64), floorDiv(freeCPU, needCPU), floorDiv(freeMem, needMem))
			a.Free = int32(min(max(free, 0), int64(a.Total)))
		}
		it.Status.Total += a.Total
		it.Status.Free += a.Free
		it.Status.Nodes = append(it.Status.Nodes, a)
	}
	return it
}
