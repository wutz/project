package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/wutz/project/api/v1alpha1"
	"github.com/wutz/project/pkg/instancetype"
	"github.com/wutz/project/pkg/kf"
	"github.com/wutz/project/pkg/naming"
	"github.com/wutz/project/pkg/platform"
)

const (
	timeout    = 20 * time.Second
	interval   = 200 * time.Millisecond
	gpuProduct = "NVIDIA-GeForce-RTX-5090"
)

// createInstanceType creates an InstanceType as the InstanceType controller
// derives it from a GPU node pool. The controller may have created it first,
// with the same spec, so an existing one is kept.
func createInstanceType(name, class, product string, gpus int64, cpu, memory string) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": instancetype.GVK.GroupVersion().String(),
		"kind":       instancetype.GVK.Kind,
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"cpu": cpu, "memory": memory,
			"gpu":          map[string]any{"class": class, "product": product, "count": gpus},
			"nodeSelector": map[string]any{naming.LabelGPUProduct: product},
		},
	}}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, u))).To(Succeed())
}

func get(obj client.Object, ns, name string) func() error {
	return func() error { return k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj) }
}

var _ = Describe("project operator", Ordered, func() {
	const project = "demo"
	ns := "project-" + project

	BeforeAll(func() {
		// Two GPUs of 8 cores and 64 GiB each per node leave 15 cores and 120 GiB per GPU.
		createInstanceType("rtx-5090.x2", "rtx-5090", gpuProduct, 2, "30", "240Gi")
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

		// The defaulted requests satisfy the quota. A default limit would apply
		// to every container that declares none and reject any request above it,
		// which is how a 4-CPU router pod used to be refused.
		lr := &corev1.LimitRange{}
		Expect(get(lr, ns, naming.LimitRangeName)()).To(Succeed())
		Expect(lr.Spec.Limits).To(HaveLen(1))
		Expect(lr.Spec.Limits[0].DefaultRequest).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("100m")))
		Expect(lr.Spec.Limits[0].Default).To(BeEmpty())

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
		Expect(fmt.Sprint(flavors)).To(ContainSubstring("project-rtx-5090"))
		Expect(fmt.Sprint(flavors)).To(ContainSubstring(naming.ResourceFlavor("cpu")))
		cpuFlavor := &unstructured.Unstructured{}
		cpuFlavor.SetGroupVersionKind(kf.ResourceFlavorGVK)
		Expect(get(cpuFlavor, "", naming.ResourceFlavor("cpu"))()).To(Succeed())
		lq := &unstructured.Unstructured{}
		lq.SetGroupVersionKind(kf.LocalQueueGVK)
		Expect(get(lq, ns, "default")()).To(Succeed())

		Expect(p.Status.Members).To(HaveLen(2))
		sa := &corev1.ServiceAccount{}
		Expect(get(sa, "project-members", "demo.alice")()).To(Succeed())
		rb := &rbacv1.RoleBinding{}
		Expect(get(rb, ns, "project-member-bob")()).To(Succeed())
		Expect(rb.RoleRef.Name).To(Equal("project-view"))
		crb := &rbacv1.ClusterRoleBinding{}
		Expect(get(crb, "", "project:project:demo")()).To(Succeed())
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

	It("issues the robot again when Harbor lost it", func() {
		reg := &corev1.Secret{}
		Expect(get(reg, ns, naming.RegistrySecretName)()).To(Succeed())
		login, oldSecret := string(reg.Data["username"]), string(reg.Data["password"])
		fakeHarbor.RemoveRobot(login)

		// A spec change makes the registry controller reconcile the project.
		p := &v1.Project{}
		Expect(get(p, "", project)()).To(Succeed())
		p.Spec.Quota.RegistryStorage = resource.NewQuantity(10<<30, resource.BinarySI)
		Expect(k8sClient.Update(ctx, p)).To(Succeed())

		Eventually(func() bool {
			cur := &corev1.Secret{}
			if get(cur, ns, naming.RegistrySecretName)() != nil {
				return false
			}
			newSecret := string(cur.Data["password"])
			return newSecret != oldSecret && fakeHarbor.RobotSecret(login) == newSecret
		}, timeout, interval).Should(BeTrue())
	})

	It("updates members and quota", func() {
		p := &v1.Project{}
		Expect(get(p, "", project)()).To(Succeed())
		p.Spec.Members = p.Spec.Members[:1]
		p.Spec.Network.Isolation = v1.NetworkNone
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(get(&corev1.ServiceAccount{}, "project-members", "demo.bob")())
		}, timeout, interval).Should(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(get(&rbacv1.RoleBinding{}, ns, "project-member-bob")())
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
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "project-taken"}})).To(Succeed())
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

	It("rebinds a member whose role changed", func() {
		p := &v1.Project{}
		Expect(get(p, "", project)()).To(Succeed())
		p.Spec.Members[0].Role = v1.RoleView
		Expect(k8sClient.Update(ctx, p)).To(Succeed())
		Eventually(func() string {
			rb := &rbacv1.RoleBinding{}
			_ = get(rb, ns, "project-member-alice")()
			return rb.RoleRef.Name
		}, timeout, interval).Should(Equal("project-view"))
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
		Expect(apierrors.IsNotFound(get(&rbacv1.ClusterRole{}, "", "project:project:demo")())).To(BeTrue())
	})
})
