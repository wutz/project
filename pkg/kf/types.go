// Package kf names the Kubeflow and Kueue APIs the Project controller uses.
// Kueue objects are exchanged as unstructured, so only their GVKs are modelled.
package kf

import "k8s.io/apimachinery/pkg/runtime/schema"

// Group of the Kubeflow Trainer v2 API, which project members may read.
const Group = "trainer.kubeflow.org"

var (
	KueueGroupVersion = schema.GroupVersion{Group: "kueue.x-k8s.io", Version: "v1beta2"}
	LocalQueueGVK     = KueueGroupVersion.WithKind("LocalQueue")
	ClusterQueueGVK   = KueueGroupVersion.WithKind("ClusterQueue")
	ResourceFlavorGVK = KueueGroupVersion.WithKind("ResourceFlavor")
)
