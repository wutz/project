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
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/wutz/forgeai/api/v1alpha1"
	"github.com/wutz/forgeai/pkg/instancetype"
	"github.com/wutz/forgeai/pkg/kf"
	"github.com/wutz/forgeai/pkg/naming"
	"github.com/wutz/forgeai/pkg/platform"
	"github.com/wutz/forgeai/pkg/sandbox"
)

const (
	fieldOwner       = client.FieldOwner("forgeai")
	projectFinalizer = "forgeai.wutz.dev/project"
	kindClusterRole  = "ClusterRole"
	keyNominalQuota  = "nominalQuota"
	verbGet          = "get"
	verbWatch        = "watch"
	verbList         = "list"
)

// ProjectReconciler turns a Project into a labelled namespace with quota,
// network isolation, a Kueue queue and member credentials.
type ProjectReconciler struct {
	client.Client
	// APIReader reads objects outside the label-filtered cache.
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Config    *platform.Loader
}

// +kubebuilder:rbac:groups=forgeai.wutz.dev,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=forgeai.wutz.dev,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=forgeai.wutz.dev,resources=projects/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces;serviceaccounts;secrets;resourcequotas;limitranges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete;bind;escalate
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=clusterqueues;localqueues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgeai.wutz.dev,resources=instancetypes,verbs=get;list;watch
// The member roles bound by the operator (it may only grant what it holds):
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods;pods/log;events;configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=trainjobs;trainingruntimes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=trainer.kubeflow.org,resources=clustertrainingruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=jobset.x-k8s.io,resources=jobsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=workloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=forgeai.wutz.dev,resources=fileservers,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes/status,verbs=get
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxtemplates;sandboxclaims;sandboxwarmpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=selfsubjectaccessreviews;selfsubjectrulesreviews,verbs=create

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	p := &v1.Project{}
	if err := r.Get(ctx, req.NamespacedName, p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	cfg, err := r.Config.Get(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	ns := naming.Namespace(cfg.NamespacePrefix, p.Name)

	if !p.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(p, projectFinalizer) {
			return ctrl.Result{}, nil
		}
		done, err := r.finalize(ctx, p, cfg, ns)
		if err != nil || !done {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
		controllerutil.RemoveFinalizer(p, projectFinalizer)
		return ctrl.Result{}, r.Update(ctx, p)
	}
	if controllerutil.AddFinalizer(p, projectFinalizer) {
		if err := r.Update(ctx, p); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := p.DeepCopy()
	p.Status.Namespace = ns
	p.Status.ObservedGeneration = p.Generation
	if err := r.reconcileProject(ctx, p, cfg, ns); err != nil {
		if _, ok := errors.AsType[*namespaceConflictError](err); ok {
			setCondition(p, v1.ConditionReady, metav1.ConditionFalse, "NamespaceConflict", err.Error())
			return ctrl.Result{}, r.Status().Patch(ctx, p, statusPatch(orig))
		}
		setCondition(p, v1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		_ = r.Status().Patch(ctx, p, statusPatch(orig))
		return ctrl.Result{}, err
	}
	setCondition(p, v1.ConditionReady, metav1.ConditionTrue, "Ready", "")
	if !meta.IsStatusConditionTrue(orig.Status.Conditions, v1.ConditionReady) {
		log.Info("Project is ready", "namespace", ns)
	}
	return ctrl.Result{}, r.Status().Patch(ctx, p, statusPatch(orig))
}

func setCondition(p *v1.Project, t string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
}

type namespaceConflictError struct{ ns string }

func (e *namespaceConflictError) Error() string {
	return fmt.Sprintf("namespace %s exists and does not belong to this project", e.ns)
}

func projectLabels(p *v1.Project) map[string]string {
	return map[string]string{naming.LabelManagedBy: naming.ManagedBy, naming.LabelProject: p.Name}
}

// apply server-side applies obj (typed or unstructured) as the forgeai field owner.
func apply(ctx context.Context, c client.Client, obj client.Object) error {
	u := &unstructured.Unstructured{}
	if uo, ok := obj.(*unstructured.Unstructured); ok {
		u = uo
	} else {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return err
		}
		u.Object = m
		gvk, err := c.GroupVersionKindFor(obj)
		if err != nil {
			return err
		}
		u.SetGroupVersionKind(gvk)
	}
	unstructured.RemoveNestedField(u.Object, naming.KeyMetadata, "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, naming.KeyStatus)
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), fieldOwner, client.ForceOwnership)
}

// dropStaleRoleBinding deletes a member's RoleBinding when it names another
// role. A RoleBinding's roleRef cannot be changed in place, so the binding is
// applied again after the delete, with the role the member now has.
func (r *ProjectReconciler) dropStaleRoleBinding(ctx context.Context, name, ns string, want rbacv1.RoleRef) error {
	rb := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, rb)
	if apierrors.IsNotFound(err) || (err == nil && rb.RoleRef == want) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.Delete(ctx, rb); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ProjectReconciler) reconcileProject(ctx context.Context, p *v1.Project, cfg *platform.Config, ns string) error {
	if err := r.ensureNamespace(ctx, p, ns); err != nil {
		return err
	}
	classes, err := r.gpuClasses(ctx)
	if err != nil {
		return err
	}
	platformConfig, err := r.platformConfig(ctx, p, ns)
	if err != nil {
		return err
	}
	objs := []client.Object{platformConfig, resourceQuota(p, cfg, ns, classes), limitRange(p, ns), localQueue(p, ns)}
	if np := networkPolicy(p, cfg, ns); np != nil {
		objs = append(objs, np)
	}
	for _, obj := range objs {
		if err := apply(ctx, r.Client, obj); err != nil {
			return fmt.Errorf("apply %T: %w", obj, err)
		}
	}
	if p.Spec.Network.Isolation == v1.NetworkNone {
		if err := client.IgnoreNotFound(r.Delete(ctx, &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: naming.NetworkPolicyName, Namespace: ns},
		})); err != nil {
			return err
		}
	}
	if err := apply(ctx, r.Client, cpuResourceFlavor()); err != nil {
		return fmt.Errorf("apply CPU ResourceFlavor: %w", err)
	}
	if err := apply(ctx, r.Client, clusterQueue(p, cfg, ns, classes)); err != nil {
		return fmt.Errorf("apply ClusterQueue: %w", err)
	}
	p.Status.ClusterQueue = naming.ClusterQueue(ns)
	p.Status.LocalQueue = naming.LocalQueueName
	return r.reconcileMembers(ctx, p, cfg, ns)
}

func (r *ProjectReconciler) ensureNamespace(ctx context.Context, p *v1.Project, ns string) error {
	cur := &corev1.Namespace{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Name: ns}, cur)
	if err == nil && cur.Labels[naming.LabelProject] != p.Name {
		return &namespaceConflictError{ns: ns}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	labels := projectLabels(p)
	// Restricted pod security: no privileged or host access for project pods.
	labels["pod-security.kubernetes.io/enforce"] = "baseline"
	labels["pod-security.kubernetes.io/warn"] = "restricted"
	return apply(ctx, r.Client, &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels},
	})
}

// platformConfig mirrors the platform ConfigMap into the project namespace:
// members read it there (storage types, registry, training defaults) and
// have no access to the platform namespace. It is read uncached so a change
// is copied by the reconcile it triggers.
func (r *ProjectReconciler) platformConfig(ctx context.Context, p *v1.Project, ns string) (*corev1.ConfigMap, error) {
	src := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.Config.Namespace, Name: platform.ConfigMapName}
	if err := r.APIReader.Get(ctx, key, src); err != nil {
		return nil, fmt.Errorf("get platform config: %w", err)
	}
	return &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: platform.ConfigMapName, Namespace: ns, Labels: projectLabels(p)},
		Data:       map[string]string{platform.ConfigKey: src.Data[platform.ConfigKey]},
	}, nil
}

// gpuClasses returns the GPU classes and their per-GPU slice from the InstanceTypes.
func (r *ProjectReconciler) gpuClasses(ctx context.Context) ([]instancetype.Class, error) {
	list := &v1.InstanceTypeList{}
	if err := r.List(ctx, list); err != nil {
		return nil, err
	}
	byName := map[string]*instancetype.Class{}
	for _, it := range list.Items {
		g := it.Spec.GPU
		if g.Count < 1 {
			continue
		}
		c := byName[g.Class]
		if c == nil {
			c = &instancetype.Class{Name: g.Class, Product: g.Product}
			byName[g.Class] = c
		}
		if g.Count == 1 || c.MilliCPU == 0 {
			c.MilliCPU = it.Spec.CPU.MilliValue() / int64(g.Count)
			c.Memory = it.Spec.Memory.Value() / int64(g.Count)
		}
	}
	out := make([]instancetype.Class, 0, len(byName))
	for _, c := range byName {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b instancetype.Class) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// gpuQuota is the GPU quota of a class: the Project's value or the default.
func gpuQuota(p *v1.Project, cfg *platform.Config, class string) int64 {
	for _, q := range p.Spec.Quota.GPUs {
		if q.Class == class {
			return int64(q.Count)
		}
	}
	return int64(cfg.DefaultQuota.GPUCount(class))
}

func resourceQuota(p *v1.Project, cfg *platform.Config, ns string, classes []instancetype.Class) *corev1.ResourceQuota {
	q := p.Spec.Quota
	storage := cfg.DefaultQuota.Storage
	if q.Storage != nil {
		storage = *q.Storage
	}
	volumes, pods := cfg.DefaultQuota.Volumes, cfg.DefaultQuota.Pods
	if q.Volumes != nil {
		volumes = *q.Volumes
	}
	if q.Pods != nil {
		pods = *q.Pods
	}
	// GPU jobs are admitted by Kueue; the ResourceQuota is the backstop for
	// workloads that bypass the queue, so it covers the same totals plus headroom.
	var gpus, milliCPU, mem int64
	for _, c := range classes {
		n := gpuQuota(p, cfg, c.Name)
		gpus += n
		milliCPU += n * c.MilliCPU
		mem += n * c.Memory
	}
	milliCPU += cfg.DefaultQuota.Headroom.CPU.MilliValue()
	mem += cfg.DefaultQuota.Headroom.Memory.Value()
	return &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Name: naming.ResourceQuotaName, Namespace: ns, Labels: projectLabels(p)},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
			corev1.ResourceRequestsStorage:         storage,
			corev1.ResourcePersistentVolumeClaims:  *resource.NewQuantity(int64(volumes), resource.DecimalSI),
			corev1.ResourcePods:                    *resource.NewQuantity(int64(pods), resource.DecimalSI),
			corev1.ResourceRequestsCPU:             *resource.NewMilliQuantity(milliCPU, resource.DecimalSI),
			corev1.ResourceRequestsMemory:          *resource.NewQuantity(mem, resource.BinarySI),
			"requests." + instancetype.ResourceGPU: *resource.NewQuantity(gpus, resource.DecimalSI),
		}},
	}
}

// limitRange gives containers without requests a small default, which the
// ResourceQuota requires. It sets no default limits: a default limit applies to
// every container that declares none, so the 1 CPU default that used to be here
// made the API server reject any container requesting more than 1 CPU or 1Gi
// (a request above its own limit), such as the inference router's 4 and 8 CPU.
// The quota counts requests only, so the limits bought nothing.
func limitRange(p *v1.Project, ns string) *corev1.LimitRange {
	return &corev1.LimitRange{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"},
		ObjectMeta: metav1.ObjectMeta{Name: naming.LimitRangeName, Namespace: ns, Labels: projectLabels(p)},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type: corev1.LimitTypeContainer,
			DefaultRequest: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}}},
	}
}

func nsPeer(name string) networkingv1.NetworkPolicyPeer {
	return networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{
		MatchLabels: map[string]string{corev1.LabelMetadataName: name},
	}}
}

// networkPolicy admits traffic from the same namespace and the platform
// namespaces only. With denyInternet, egress is limited to the namespace and DNS.
func networkPolicy(p *v1.Project, cfg *platform.Config, ns string) *networkingv1.NetworkPolicy {
	if p.Spec.Network.Isolation == v1.NetworkNone {
		return nil
	}
	// The agent-sandbox controller runs outside the project and talks to the
	// sandbox API server on the pod IP and to its port-forward (kubectl,
	// port 80 of the pod IP) inside the sandbox.
	agents := nsPeer(cfg.Sandbox.NamespaceOrDefault())
	same := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{}}
	from := []networkingv1.NetworkPolicyPeer{same}
	for _, n := range cfg.Network.SystemNamespaces {
		from = append(from, nsPeer(n))
	}
	from = append(from, agents)
	// The gateway proxies the ports sandboxes publish, so its pods reach them.
	if cfg.Gateway.Enabled() {
		from = append(from, nsPeer(cfg.Gateway.Namespace))
	}
	np := &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: naming.NetworkPolicyName, Namespace: ns, Labels: projectLabels(p)},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: from}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	if p.Spec.Network.DenyInternet {
		udp, tcp := corev1.ProtocolUDP, corev1.ProtocolTCP
		dns := intstr.FromInt32(53)
		np.Spec.PolicyTypes = append(np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		np.Spec.Egress = []networkingv1.NetworkPolicyEgressRule{
			{To: []networkingv1.NetworkPolicyPeer{same, agents}},
			{
				To:    []networkingv1.NetworkPolicyPeer{nsPeer(cfg.Network.DNSNamespace)},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}},
			},
		}
	}
	return np
}

func localQueue(p *v1.Project, ns string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		naming.KeyMetadata: map[string]any{naming.KeyName: naming.LocalQueueName, naming.KeyNamespace: ns, naming.KeyLabels: toAny(projectLabels(p))},
		naming.KeySpec:     map[string]any{"clusterQueue": naming.ClusterQueue(ns)},
	}}
	u.SetGroupVersionKind(kf.LocalQueueGVK)
	return u
}

func toAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// cpuFlavor is the ResourceFlavor of workloads that request no GPU. It has no
// node labels, so those pods run on any node.
const cpuFlavor = "cpu"

func cpuResourceFlavor() *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		naming.KeyMetadata: map[string]any{
			naming.KeyName:   naming.ResourceFlavor(cpuFlavor),
			naming.KeyLabels: map[string]any{naming.LabelManagedBy: naming.ManagedBy},
		},
	}}
	u.SetGroupVersionKind(kf.ResourceFlavorGVK)
	return u
}

// flavor is one ResourceFlavor entry of a ClusterQueue resource group.
func flavor(name string, milliCPU, memory, gpus int64) map[string]any {
	return map[string]any{
		naming.KeyName: name,
		"resources": []any{
			map[string]any{naming.KeyName: "cpu", keyNominalQuota: resource.NewMilliQuantity(milliCPU, resource.DecimalSI).String()},
			map[string]any{naming.KeyName: "memory", keyNominalQuota: resource.NewQuantity(memory, resource.BinarySI).String()},
			map[string]any{naming.KeyName: string(instancetype.ResourceGPU), keyNominalQuota: fmt.Sprint(gpus)},
		},
	}
}

// clusterQueue is the project's Kueue ClusterQueue: one ResourceFlavor per
// GPU class, with CPU and memory quota following the per-GPU slice. The CPU
// flavor comes last and holds the headroom, so workloads without a GPU are
// admitted even when the project has no GPU quota or the cluster has no GPUs.
func clusterQueue(p *v1.Project, cfg *platform.Config, ns string, classes []instancetype.Class) *unstructured.Unstructured {
	flavors := make([]any, 0, len(classes)+1)
	for _, c := range classes {
		n := gpuQuota(p, cfg, c.Name)
		flavors = append(flavors, flavor(naming.ResourceFlavor(c.Name), n*c.MilliCPU, n*c.Memory, n))
	}
	headroom := cfg.DefaultQuota.Headroom
	flavors = append(flavors, flavor(naming.ResourceFlavor(cpuFlavor), headroom.CPU.MilliValue(), headroom.Memory.Value(), 0))
	groups := []any{map[string]any{
		"coveredResources": []any{"cpu", "memory", string(instancetype.ResourceGPU)},
		"flavors":          flavors,
	}}
	u := &unstructured.Unstructured{Object: map[string]any{
		naming.KeyMetadata: map[string]any{
			naming.KeyName: naming.ClusterQueue(ns), naming.KeyLabels: toAny(projectLabels(p)),
			"ownerReferences": []any{ownerRef(p)},
		},
		"spec": map[string]any{
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{corev1.LabelMetadataName: ns}},
			"queueingStrategy":  "BestEffortFIFO",
			"resourceGroups":    groups,
		},
	}}
	u.SetGroupVersionKind(kf.ClusterQueueGVK)
	return u
}

func ownerRef(p *v1.Project) map[string]any {
	return map[string]any{
		naming.KeyAPIVersion: v1.GroupVersion.String(), naming.KeyKind: "Project", naming.KeyName: p.Name, "uid": string(p.UID),
		"controller": true, "blockOwnerDeletion": true,
	}
}

func ownerRefs(p *v1.Project) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: v1.GroupVersion.String(), Kind: "Project", Name: p.Name, UID: p.UID,
		Controller: new(true), BlockOwnerDeletion: new(true),
	}}
}

// reconcileMembers creates a ServiceAccount, a long-lived token and a role
// binding per member, and prunes removed members.
func (r *ProjectReconciler) reconcileMembers(ctx context.Context, p *v1.Project, cfg *platform.Config, ns string) error {
	mns := cfg.MembersNamespace
	if err := apply(ctx, r.Client, &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: mns, Labels: map[string]string{
			naming.LabelManagedBy: naming.ManagedBy, "pod-security.kubernetes.io/enforce": "restricted",
		}},
	}); err != nil {
		return err
	}

	labels := projectLabels(p)
	want := map[string]bool{}
	subjects := []rbacv1.Subject{}
	status := []v1.MemberStatus{}
	for _, m := range p.Spec.Members {
		role := m.Role
		if role == "" {
			role = v1.RoleEdit
		}
		sa := naming.MemberServiceAccount(p.Name, m.Name)
		want[sa] = true
		ml := projectLabels(p)
		ml[naming.LabelMember] = m.Name
		roleRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: naming.ClusterRolePrefix + string(role)}
		if err := r.dropStaleRoleBinding(ctx, naming.MemberRoleBinding(m.Name), ns, roleRef); err != nil {
			return fmt.Errorf("member %s: %w", m.Name, err)
		}
		objs := []client.Object{
			&corev1.ServiceAccount{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
				ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: mns, Labels: ml, OwnerReferences: ownerRefs(p)},
				// Member tokens are only used from outside the cluster.
				AutomountServiceAccountToken: new(false),
			},
			&corev1.Secret{
				TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{
					Name: naming.MemberTokenSecret(p.Name, m.Name), Namespace: mns, Labels: ml,
					Annotations: map[string]string{corev1.ServiceAccountNameKey: sa},
				},
				Type: corev1.SecretTypeServiceAccountToken,
			},
			&rbacv1.RoleBinding{
				TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "RoleBinding"},
				ObjectMeta: metav1.ObjectMeta{Name: naming.MemberRoleBinding(m.Name), Namespace: ns, Labels: ml, OwnerReferences: ownerRefs(p)},
				RoleRef:    roleRef,
				Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: mns}},
			},
		}
		for _, o := range objs {
			if err := apply(ctx, r.Client, o); err != nil {
				return fmt.Errorf("member %s: %w", m.Name, err)
			}
		}
		subjects = append(subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: mns})
		status = append(status, v1.MemberStatus{
			Name: m.Name, Namespace: mns, ServiceAccount: sa, TokenSecret: naming.MemberTokenSecret(p.Name, m.Name),
		})
	}
	p.Status.Members = status

	role := naming.ProjectClusterRole(p.Name)
	if err := apply(ctx, r.Client, &rbacv1.ClusterRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: role, Labels: labels, OwnerReferences: ownerRefs(p)},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{v1.GroupVersion.Group}, Resources: []string{"projects"}, ResourceNames: []string{p.Name}, Verbs: []string{verbGet, verbWatch}},
			{APIGroups: []string{""}, Resources: []string{"namespaces"}, ResourceNames: []string{ns}, Verbs: []string{verbGet}},
			{APIGroups: []string{"kueue.x-k8s.io"}, Resources: []string{"clusterqueues"}, ResourceNames: []string{naming.ClusterQueue(ns)}, Verbs: []string{verbGet, verbWatch}},
			{APIGroups: []string{v1.GroupVersion.Group}, Resources: []string{"instancetypes"}, Verbs: []string{verbGet, verbList, verbWatch}},
			{APIGroups: []string{kf.Group}, Resources: []string{"clustertrainingruntimes"}, Verbs: []string{verbGet, verbList, verbWatch}},
			{APIGroups: []string{sandbox.Group}, Resources: []string{"sandboxes"}, Verbs: []string{verbGet, verbList, verbWatch}},
			{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"}, Verbs: []string{"create"}},
		},
	}); err != nil {
		return err
	}
	if err := apply(ctx, r.Client, &rbacv1.ClusterRoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: role, Labels: labels, OwnerReferences: ownerRefs(p)},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: role},
		Subjects:   subjects,
	}); err != nil {
		return err
	}
	return r.pruneMembers(ctx, p, mns, ns, want)
}

func (r *ProjectReconciler) pruneMembers(ctx context.Context, p *v1.Project, mns, ns string, want map[string]bool) error {
	sel := client.MatchingLabels{naming.LabelProject: p.Name}
	sas := &corev1.ServiceAccountList{}
	if err := r.List(ctx, sas, client.InNamespace(mns), sel); err != nil {
		return err
	}
	for i := range sas.Items {
		if !want[sas.Items[i].Name] {
			// Deleting the ServiceAccount invalidates its token Secret.
			if err := client.IgnoreNotFound(r.Delete(ctx, &sas.Items[i])); err != nil {
				return err
			}
		}
	}
	secrets := &corev1.SecretList{}
	if err := r.List(ctx, secrets, client.InNamespace(mns), sel); err != nil {
		return err
	}
	for i := range secrets.Items {
		if !want[secrets.Items[i].Annotations[corev1.ServiceAccountNameKey]] {
			if err := client.IgnoreNotFound(r.Delete(ctx, &secrets.Items[i])); err != nil {
				return err
			}
		}
	}
	rbs := &rbacv1.RoleBindingList{}
	if err := r.List(ctx, rbs, client.InNamespace(ns), sel); err != nil {
		return err
	}
	for i := range rbs.Items {
		m := rbs.Items[i].Labels[naming.LabelMember]
		if m != "" && !want[naming.MemberServiceAccount(p.Name, m)] {
			if err := client.IgnoreNotFound(r.Delete(ctx, &rbs.Items[i])); err != nil {
				return err
			}
		}
	}
	return nil
}

// finalize revokes member access and, with deletionPolicy Delete, removes
// the namespace. It reports whether cleanup is complete.
func (r *ProjectReconciler) finalize(ctx context.Context, p *v1.Project, cfg *platform.Config, ns string) (bool, error) {
	log := logf.FromContext(ctx)
	if err := r.pruneMembers(ctx, p, cfg.MembersNamespace, ns, map[string]bool{}); err != nil {
		return false, err
	}
	role := naming.ProjectClusterRole(p.Name)
	cq := &unstructured.Unstructured{}
	cq.SetGroupVersionKind(kf.ClusterQueueGVK)
	cq.SetName(naming.ClusterQueue(ns))
	for _, o := range []client.Object{
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: role}},
		cq,
	} {
		if err := r.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return false, err
		}
	}
	if p.Spec.DeletionPolicy != v1.DeletionDelete {
		return true, nil
	}
	cur := &corev1.Namespace{}
	err := r.APIReader.Get(ctx, types.NamespacedName{Name: ns}, cur)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if cur.Labels[naming.LabelProject] != p.Name {
		return true, nil
	}
	if cur.DeletionTimestamp.IsZero() {
		log.Info("Deleting project Namespace", "namespace", ns)
		if err := r.Delete(ctx, cur); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	// The registry finalizer may need the namespace's secrets gone first; we
	// do not wait for the namespace to disappear.
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	lq := &unstructured.Unstructured{}
	lq.SetGroupVersionKind(kf.LocalQueueGVK)
	cq := &unstructured.Unstructured{}
	cq.SetGroupVersionKind(kf.ClusterQueueGVK)
	byProjectLabel := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if p := o.GetLabels()[naming.LabelProject]; p != "" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: p}}}
		}
		return nil
	})
	managed := builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetLabels()[naming.LabelProject] != ""
	}))
	allProjects := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
		list := &v1.ProjectList{}
		if err := mgr.GetClient().List(ctx, list); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(list.Items))
		for _, p := range list.Items {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: p.Name}})
		}
		return out
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Project{}).
		Watches(&corev1.Namespace{}, byProjectLabel, managed).
		Watches(&corev1.ResourceQuota{}, byProjectLabel, managed).
		Watches(&corev1.LimitRange{}, byProjectLabel, managed).
		Watches(&networkingv1.NetworkPolicy{}, byProjectLabel, managed).
		Watches(&rbacv1.RoleBinding{}, byProjectLabel, managed).
		Watches(&corev1.ServiceAccount{}, byProjectLabel, managed).
		Watches(lq, byProjectLabel, managed).
		Watches(cq, byProjectLabel, managed).
		// Quotas follow the GPU classes and their slices.
		Watches(&v1.InstanceType{}, allProjects, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Agent sandboxes carry the project label, so a new one is a new
		// member of the project (nothing to reconcile yet, but the watch
		// documents ownership and keeps quotas in sync on changes).
		Watches(projectSandboxObject(), byProjectLabel, managed).
		// Project namespaces carry a copy of the platform config.
		Watches(&corev1.ConfigMap{}, allProjects, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == r.Config.Namespace && o.GetName() == platform.ConfigMapName
		}))).
		Named("project").
		Complete(r)
}

// projectSandboxObject is an empty Sandbox for the project controller's watch.
func projectSandboxObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(sandbox.SandboxGVK)
	return u
}

// statusPatch patches Project status with optimistic locking: the project
// and registry controllers both write conditions, and a merge patch replaces
// the whole list, so a stale writer must conflict and retry.
func statusPatch(orig *v1.Project) client.Patch {
	return client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})
}
