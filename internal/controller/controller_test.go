package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/wutz/forgeai/api/v1alpha1"
	"github.com/wutz/forgeai/pkg/kf"
	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
	"github.com/wutz/forgeai/pkg/training"
	"github.com/wutz/forgeai/pkg/volumes"
)

const (
	timeout    = 20 * time.Second
	interval   = 200 * time.Millisecond
	gpuProduct = "NVIDIA-GeForce-RTX-5090"
)

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

func get(obj client.Object, ns, name string) func() error {
	return func() error { return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj) }
}

func trainJob(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(kf.TrainJobGVK)
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u)).To(Succeed())
	return u
}

var _ = Describe("forgeai operator", Ordered, func() {
	const project = "demo"
	ns := "forge-" + project

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
		}, timeout, interval).Should(Equal(int32(8)), func() string {
			n := &corev1.Node{}
			_ = get(n, "", "gn-1")()
			return fmt.Sprintf("%+v\n%+v\n%+v", it.Status, n.Status.Conditions, n.Spec.Taints)
		})
		Expect(it.Status.Free).To(Equal(int32(8)))

		rf := &unstructured.Unstructured{}
		rf.SetGroupVersionKind(kf.ResourceFlavorGVK)
		Eventually(get(rf, "", "forgeai-rtx-5090"), timeout, interval).Should(Succeed())

		// Manually deleted instance types come back.
		Expect(k8sClient.Delete(ctx, it)).To(Succeed())
		Eventually(func() bool {
			cur := &v1.InstanceType{}
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(it), cur) == nil && cur.UID != it.UID
		}, timeout, interval).Should(BeTrue())
	})

	It("sets up a project namespace with quota, isolation, queue, members and registry", func() {
		p := &v1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: project},
			Spec: v1.ProjectSpec{
				Quota:   v1.ProjectQuota{GPUs: []v1.GPUQuota{{Class: "rtx-5090", Count: 2}}},
				Network: v1.ProjectNetwork{Isolation: v1.NetworkIsolated, DenyInternet: true},
				Members: []v1.Member{{Name: "alice", Role: v1.RoleEdit}, {Name: "bob", Role: v1.RoleView}},
			},
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		Eventually(func() bool {
			_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)
			return meta.IsStatusConditionTrue(p.Status.Conditions, v1.ConditionReady) &&
				meta.IsStatusConditionTrue(p.Status.Conditions, v1.ConditionRegistry)
		}, timeout, interval).Should(BeTrue(), func() string { return fmt.Sprintf("%+v", p.Status) })
		Expect(p.Status.Namespace).To(Equal(ns))
		Expect(p.Status.Registry.URL).To(Equal("cr.example.com:7443/" + ns))

		nsObj := &corev1.Namespace{}
		Expect(get(nsObj, "", ns)()).To(Succeed())
		Expect(nsObj.Labels).To(HaveKeyWithValue(naming.LabelProject, project))

		rq := &corev1.ResourceQuota{}
		Expect(get(rq, ns, naming.ResourceQuotaName)()).To(Succeed())
		gpus := rq.Spec.Hard["requests.nvidia.com/gpu"]
		cpu := rq.Spec.Hard[corev1.ResourceRequestsCPU]
		Expect(gpus.Value()).To(Equal(int64(2)))
		// 2 GPUs x 15 cores + 4 cores headroom.
		Expect(cpu.String()).To(Equal("34"))

		// Members read the platform config from their namespace.
		pc := &corev1.ConfigMap{}
		Expect(get(pc, ns, platform.ConfigMapName)()).To(Succeed())
		Expect(pc.Data).To(HaveKeyWithValue(platform.ConfigKey, testPlatformConfig))

		np := &networkingv1.NetworkPolicy{}
		Expect(get(np, ns, naming.NetworkPolicyName)()).To(Succeed())
		Expect(np.Spec.PolicyTypes).To(ConsistOf(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))

		cq := &unstructured.Unstructured{}
		cq.SetGroupVersionKind(kf.ClusterQueueGVK)
		Expect(get(cq, "", ns)()).To(Succeed())
		flavors, _, _ := unstructured.NestedSlice(cq.Object, "spec", "resourceGroups")
		Expect(fmt.Sprint(flavors)).To(ContainSubstring("forgeai-rtx-5090"))
		lq := &unstructured.Unstructured{}
		lq.SetGroupVersionKind(kf.LocalQueueGVK)
		Expect(get(lq, ns, "default")()).To(Succeed())

		Expect(p.Status.Members).To(HaveLen(2))
		sa := &corev1.ServiceAccount{}
		Expect(get(sa, "forgeai-members", "demo.alice")()).To(Succeed())
		rb := &rbacv1.RoleBinding{}
		Expect(get(rb, ns, "forgeai-member-bob")()).To(Succeed())
		Expect(rb.RoleRef.Name).To(Equal("forgeai-project-view"))
		crb := &rbacv1.ClusterRoleBinding{}
		Expect(get(crb, "", "forgeai:project:demo")()).To(Succeed())
		Expect(crb.Subjects).To(HaveLen(2))

		Expect(fakeHarbor.HasProject(ns)).To(BeTrue())
		reg := &corev1.Secret{}
		Expect(get(reg, ns, naming.RegistrySecretName)()).To(Succeed())
		Expect(reg.Type).To(Equal(corev1.SecretTypeDockerConfigJson))
		Expect(fakeHarbor.RobotSecret(string(reg.Data["username"]))).To(Equal(string(reg.Data["password"])))
		def := &corev1.ServiceAccount{}
		Eventually(func() []corev1.LocalObjectReference {
			_ = get(def, ns, "default")()
			return def.ImagePullSecrets
		}, timeout, interval).Should(ContainElement(corev1.LocalObjectReference{Name: naming.RegistrySecretName}))
	})

	It("updates members and quota", func() {
		p := &v1.Project{}
		Expect(get(p, "", project)()).To(Succeed())
		p.Spec.Members = p.Spec.Members[:1]
		p.Spec.Network.Isolation = v1.NetworkNone
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(get(&corev1.ServiceAccount{}, "forgeai-members", "demo.bob")())
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(get(&rbacv1.RoleBinding{}, ns, "forgeai-member-bob")())
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(get(&networkingv1.NetworkPolicy{}, ns, naming.NetworkPolicyName)())
		}, timeout, interval).Should(BeTrue())
	})

	It("copies platform config changes into project namespaces", func() {
		src := &corev1.ConfigMap{}
		Expect(get(src, configNamespace, platform.ConfigMapName)()).To(Succeed())
		changed := testPlatformConfig + "# edited\n"
		src.Data[platform.ConfigKey] = changed
		Expect(k8sClient.Update(ctx, src)).To(Succeed())
		DeferCleanup(func() {
			Expect(get(src, configNamespace, platform.ConfigMapName)()).To(Succeed())
			src.Data[platform.ConfigKey] = testPlatformConfig
			Expect(k8sClient.Update(ctx, src)).To(Succeed())
		})
		Eventually(func() string {
			pc := &corev1.ConfigMap{}
			_ = get(pc, ns, platform.ConfigMapName)()
			return pc.Data[platform.ConfigKey]
		}, timeout, interval).Should(Equal(changed))
	})

	It("refuses to adopt a foreign namespace", func() {
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "forge-taken"}})).To(Succeed())
		p := &v1.Project{ObjectMeta: metav1.ObjectMeta{Name: "taken"}}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		Eventually(func() string {
			_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(p), p)
			if c := meta.FindStatusCondition(p.Status.Conditions, v1.ConditionReady); c != nil {
				return c.Reason
			}
			return ""
		}, timeout, interval).Should(Equal("NamespaceConflict"))
	})

	It("purges expired volumes from the trash", func() {
		pcfg, err := platform.Parse([]byte(testPlatformConfig))
		Expect(err).NotTo(HaveOccurred())
		vc := &volumes.Client{K8s: k8sClient, Namespace: ns}
		_, err = vc.Create(ctx, pcfg, "keep", "", 10)
		Expect(err).NotTo(HaveOccurred())
		_, err = vc.Create(ctx, pcfg, "scratch", "", 10)
		Expect(err).NotTo(HaveOccurred())
		_, err = vc.Trash(ctx, "keep", time.Hour)
		Expect(err).NotTo(HaveOccurred())
		_, err = vc.Trash(ctx, "scratch", time.Second)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			pvc := &corev1.PersistentVolumeClaim{}
			err := get(pvc, ns, "scratch")()
			// envtest has no PVC protection controller, but kube-controller-manager
			// would add a finalizer; either way deletion has been requested.
			return apierrors.IsNotFound(err) || (err == nil && !pvc.DeletionTimestamp.IsZero())
		}, timeout, interval).Should(BeTrue())
		Consistently(get(&corev1.PersistentVolumeClaim{}, ns, "keep"), 2*time.Second, interval).Should(Succeed())
		_, err = vc.Restore(ctx, "keep")
		Expect(err).NotTo(HaveOccurred())
	})

	It("runs the file server while active and stops it when idle", func() {
		fs := &v1.FileServer{ObjectMeta: metav1.ObjectMeta{Name: naming.FileServerName, Namespace: ns, Annotations: map[string]string{
			naming.AnnotationLastActive: time.Now().UTC().Format(time.RFC3339),
		}}, Spec: v1.FileServerSpec{IdleTimeout: &metav1.Duration{Duration: 3 * time.Second}}}
		Expect(k8sClient.Create(ctx, fs)).To(Succeed())
		dep := &appsv1.Deployment{}
		name := "forgeai-files-default"
		Eventually(func() int32 {
			if err := get(dep, ns, name)(); err != nil {
				return -1
			}
			return *dep.Spec.Replicas
		}, timeout, interval).Should(Equal(int32(1)))
		Expect(dep.Spec.Template.Annotations[annotationServedVolumes]).To(Equal("keep"))
		ing := &networkingv1.Ingress{}
		Expect(get(ing, ns, name)()).To(Succeed())
		Expect(ing.Spec.Rules[0].Host).To(Equal("files-forge-demo.example.com"))
		// The status is patched after the workload, so poll for it.
		Eventually(func() string {
			_ = get(fs, ns, fs.Name)()
			return fs.Status.URL
		}, timeout, interval).Should(Equal("https://files-forge-demo.example.com:7443"))
		Expect(get(&corev1.Secret{}, ns, naming.FileServerSecretName)()).To(Succeed())

		// No heartbeat: scaled to zero after the idle timeout.
		Eventually(func() int32 {
			_ = get(dep, ns, name)()
			return *dep.Spec.Replicas
		}, timeout, interval).Should(Equal(int32(0)))
		Eventually(func() v1.FileServerPhase {
			_ = get(fs, ns, fs.Name)()
			return fs.Status.Phase
		}, timeout, interval).Should(Equal(v1.FileServerIdle))

		// A heartbeat wakes it up again.
		fs.Annotations[naming.AnnotationLastActive] = time.Now().UTC().Format(time.RFC3339)
		Expect(k8sClient.Update(ctx, fs)).To(Succeed())
		Eventually(func() int32 {
			_ = get(dep, ns, name)()
			return *dep.Spec.Replicas
		}, timeout, interval).Should(Equal(int32(1)))
	})

	It("classifies a failed TrainJob and retries infrastructure failures", func() {
		f := &training.Form{Name: "train", Image: "img", InstanceType: "rtx-5090.x1", Workers: 1, MaxRetries: 1}
		it := &v1.InstanceType{}
		Expect(get(it, "", "rtx-5090.x1")()).To(Succeed())
		pcfg, _ := platform.Parse([]byte(testPlatformConfig))
		tj, err := training.TrainJob(f, training.Context{Namespace: ns, InstanceType: it, Config: pcfg})
		Expect(err).NotTo(HaveOccurred())
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tj)
		Expect(err).NotTo(HaveOccurred())
		u := &unstructured.Unstructured{Object: m}
		unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
		Expect(k8sClient.Create(ctx, u)).To(Succeed())

		// A worker pod killed by node failure, then the TrainJob fails.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "train-node-0-0-x", Namespace: ns, Labels: map[string]string{
				kf.LabelJobSetName: "train", kf.LabelCompletion: "0",
			}},
			Spec: corev1.PodSpec{NodeName: "gn-gone", Containers: []corev1.Container{{Name: "node", Image: "img"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		pod.Status = corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "node", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "Error"}},
		}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		u = trainJob(ns, "train")
		Expect(unstructured.SetNestedSlice(u.Object, []any{map[string]any{
			"type": "Failed", "status": "True", "reason": "FailedJobs", "message": "jobset failed",
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		}}, "status", "conditions")).To(Succeed())
		Expect(k8sClient.Status().Update(ctx, u)).To(Succeed())

		Eventually(func() map[string]string {
			return trainJob(ns, "train").GetAnnotations()
		}, timeout, interval).Should(And(
			HaveKeyWithValue(naming.AnnotationFailureReason, training.ReasonNodeFailure),
			HaveKeyWithValue(naming.AnnotationRetriedAs, "train-r1"),
		))
		retry := trainJob(ns, "train-r1")
		Expect(retry.GetLabels()).To(HaveKeyWithValue(naming.LabelJobGroup, "train"))
		Expect(retry.GetAnnotations()).To(HaveKeyWithValue(naming.AnnotationRetry, "1"))
		patches, _, _ := unstructured.NestedSlice(retry.Object, "spec", "runtimePatches")
		Expect(patches).To(HaveLen(1))
	})

	It("fails jobs stuck on image pulls without retrying them", func() {
		f := &training.Form{Name: "stuck", Image: "nope", InstanceType: "rtx-5090.x1", Workers: 1, MaxRetries: 3, RetryOn: training.RetryAny}
		it := &v1.InstanceType{}
		Expect(get(it, "", "rtx-5090.x1")()).To(Succeed())
		pcfg, _ := platform.Parse([]byte(testPlatformConfig))
		tj, _ := training.TrainJob(f, training.Context{Namespace: ns, InstanceType: it, Config: pcfg})
		m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(tj)
		u := &unstructured.Unstructured{Object: m}
		unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
		Expect(k8sClient.Create(ctx, u)).To(Succeed())

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stuck-node-0-0-x", Namespace: ns, Labels: map[string]string{
				kf.LabelJobSetName: "stuck", kf.LabelCompletion: "0",
			}},
			Spec: corev1.PodSpec{NodeName: "gn-1", Containers: []corev1.Container{{Name: "node", Image: "nope"}}},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		pod.Status = corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "node", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "not found"}},
		}}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		Eventually(func() map[string]string {
			return trainJob(ns, "stuck").GetAnnotations()
		}, timeout, interval).Should(And(
			HaveKeyWithValue(naming.AnnotationFailureReason, training.ReasonImagePullFailed),
			HaveKeyWithValue(training.AnnotationStopped, "true"),
		))
		Consistently(func() bool {
			return apierrors.IsNotFound(get(&unstructured.Unstructured{Object: map[string]any{
				"apiVersion": kf.TrainJobGVK.GroupVersion().String(), "kind": "TrainJob"}}, ns, "stuck-r1")())
		}, 2*time.Second, interval).Should(BeTrue())
	})

	It("deletes the namespace and registry with deletionPolicy Delete", func() {
		p := &v1.Project{}
		Expect(get(p, "", project)()).To(Succeed())
		p.Spec.DeletionPolicy = v1.DeletionDelete
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		Expect(k8sClient.Delete(ctx, p)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(get(&v1.Project{}, "", project)()) }, timeout, interval).Should(BeTrue())
		Expect(fakeHarbor.HasProject(ns)).To(BeFalse())
		nsObj := &corev1.Namespace{}
		Expect(get(nsObj, "", ns)()).To(Succeed())
		Expect(nsObj.DeletionTimestamp.IsZero()).To(BeFalse())
		Expect(apierrors.IsNotFound(get(&rbacv1.ClusterRole{}, "", "forgeai:project:demo")())).To(BeTrue())
	})
})
