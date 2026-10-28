/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/wutz/project/api/v1alpha1"
	"github.com/wutz/project/pkg/kf"
	"github.com/wutz/project/pkg/naming"
)

// createGPUNode adds a Ready node with eight GPUs of the same product.
func createGPUNode(name string) {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
		naming.LabelGPUProduct: gpuProduct, naming.LabelGPUMemory: "32607",
	}}}
	Expect(k8sClient.Create(ctx, n)).To(Succeed())
	n.Status = corev1.NodeStatus{
		Capacity: corev1.ResourceList{"cpu": resource.MustParse("128"), "memory": resource.MustParse("1Ti"),
			"nvidia.com/gpu": resource.MustParse("8"), "pods": resource.MustParse("110")},
		Allocatable: corev1.ResourceList{"cpu": resource.MustParse("128"), "memory": resource.MustParse("1Ti"),
			"nvidia.com/gpu": resource.MustParse("8"), "pods": resource.MustParse("110")},
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue,
			LastHeartbeatTime: metav1.Now(), LastTransitionTime: metav1.Now(), Reason: "KubeletReady"}},
	}
	Expect(k8sClient.Status().Update(ctx, n)).To(Succeed())
	// The API server taints new nodes not-ready; without a node lifecycle
	// controller in envtest the taint stays, so drop it.
	Eventually(func() error {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(n), n); err != nil {
			return err
		}
		n.Spec.Taints = nil
		return k8sClient.Update(ctx, n)
	}, timeout, interval).Should(Succeed())
}

var _ = Describe("InstanceType controller", Ordered, func() {
	BeforeAll(func() {
		createGPUNode("gn-1")
		createGPUNode("gn-2")
	})

	It("derives instance types and resource flavors from GPU nodes", func() {
		it := &v1.InstanceType{}
		Eventually(get(it, "", "rtx-5090.x2"), timeout, interval).Should(Succeed())
		// (128 - 8) / 8 = 15 cores, (1024 - 64) / 8 = 120 GiB per GPU.
		Expect(it.Spec.CPU.String()).To(Equal("30"))
		Expect(it.Spec.Memory.String()).To(Equal("240Gi"))
		Expect(it.Spec.NodeSelector).To(HaveKeyWithValue(naming.LabelGPUProduct, gpuProduct))
		Eventually(func() int32 {
			_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(it), it)
			return it.Status.Total
		}, timeout, interval).Should(Equal(int32(8)))
		Expect(it.Status.Free).To(Equal(int32(8)))

		rf := &unstructured.Unstructured{}
		rf.SetGroupVersionKind(kf.ResourceFlavorGVK)
		Eventually(get(rf, "", naming.ResourceFlavor("rtx-5090")), timeout, interval).Should(Succeed())
	})

	It("recreates an instance type that was deleted by hand", func() {
		it := &v1.InstanceType{}
		Eventually(get(it, "", "rtx-5090.x1"), timeout, interval).Should(Succeed())
		uid := it.UID
		Expect(k8sClient.Delete(ctx, it)).To(Succeed())
		Eventually(func() bool {
			cur := &v1.InstanceType{}
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(it), cur) == nil && cur.UID != uid
		}, timeout, interval).Should(BeTrue())
	})
})
