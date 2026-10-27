// Package instancetype models the forgeai InstanceType fields the Project
// controller reads. The forgeai operator owns the type and derives it from the
// GPU nodes; this operator only reads it, as unstructured.
package instancetype

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GVK of the forgeai InstanceType.
var GVK = schema.GroupVersionKind{Group: "forgeai.wutz.dev", Version: "v1alpha1", Kind: "InstanceType"}

// ResourceGPU is the NVIDIA device plugin resource.
const ResourceGPU corev1.ResourceName = "nvidia.com/gpu"

// Class is a group of InstanceTypes with the same GPU product.
type Class struct {
	Name    string
	Product string
	// MilliCPU and Memory (bytes) per GPU: the InstanceType's cpu and memory
	// divided by its GPU count, minimum over the InstanceTypes of the class.
	MilliCPU int64
	Memory   int64
}

// CPUPerGPU returns the per-GPU CPU slice as a quantity.
func (c Class) CPUPerGPU() resource.Quantity {
	return *resource.NewMilliQuantity(c.MilliCPU, resource.DecimalSI)
}

// MemoryPerGPU returns the per-GPU memory slice as a quantity.
func (c Class) MemoryPerGPU() resource.Quantity {
	return *resource.NewQuantity(c.Memory, resource.BinarySI)
}
