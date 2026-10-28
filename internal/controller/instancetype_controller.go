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
	"maps"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/wutz/project/api/v1alpha1"
	"github.com/wutz/project/pkg/instancetype"
	"github.com/wutz/project/pkg/kf"
	"github.com/wutz/project/pkg/naming"
	"github.com/wutz/project/pkg/platform"
)

// instanceTypeKey is the single reconcile request of the InstanceType
// controller: every change recomputes all instance types.
var instanceTypeKey = reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

// InstanceTypeReconciler derives InstanceTypes and Kueue ResourceFlavors
// from GPU nodes, and keeps their availability up to date from pod requests.
type InstanceTypeReconciler struct {
	client.Client
	Config *platform.Loader
	// Kueue is false when the cluster has no Kueue API; ResourceFlavors are
	// then not maintained.
	Kueue bool
	// Now is replaceable in tests.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=workspace.wutz.dev,resources=instancetypes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workspace.wutz.dev,resources=instancetypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes;pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=kueue.x-k8s.io,resources=resourceflavors,verbs=get;list;watch;create;update;patch;delete

func (r *InstanceTypeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	cfg, err := r.Config.Get(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return ctrl.Result{}, err
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		return ctrl.Result{}, err
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	classes, instances := instancetype.Compute(nodes.Items, pods.Items, cfg.InstanceTypes, metav1.NewTime(now()))

	for _, c := range classes {
		if !r.Kueue {
			break
		}
		if err := apply(ctx, r.Client, resourceFlavor(c)); err != nil {
			return ctrl.Result{}, err
		}
	}
	// Without Kueue, workloads are placed by the ResourceQuota alone.

	want := map[string]bool{}
	for i := range instances {
		it := &instances[i]
		want[it.Name] = true
		cur := &v1.InstanceType{}
		err := r.Get(ctx, client.ObjectKeyFromObject(it), cur)
		if apierrors.IsNotFound(err) {
			status := it.Status
			if err := r.Create(ctx, it); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Created InstanceType", "name", it.Name)
			it.Status = status
			cur = it.DeepCopy()
			cur.Status = v1.InstanceTypeStatus{}
		} else if err != nil {
			return ctrl.Result{}, err
		} else if !equality.Semantic.DeepEqual(cur.Spec, it.Spec) || !maps.Equal(cur.Labels, it.Labels) {
			cur.Spec, cur.Labels = it.Spec, it.Labels
			if err := r.Update(ctx, cur); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Updated InstanceType", "name", it.Name)
		}
		if statusChanged(cur.Status, it.Status) {
			orig := cur.DeepCopy()
			cur.Status = it.Status
			if err := r.Status().Patch(ctx, cur, client.MergeFrom(orig)); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	list := &v1.InstanceTypeList{}
	if err := r.List(ctx, list, client.MatchingLabels{naming.LabelManagedBy: naming.ManagedBy}); err != nil {
		return ctrl.Result{}, err
	}
	for i := range list.Items {
		if !want[list.Items[i].Name] {
			if err := client.IgnoreNotFound(r.Delete(ctx, &list.Items[i])); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Deleted InstanceType", "name", list.Items[i].Name)
		}
	}
	// ResourceFlavors are kept when a class disappears: ClusterQueues still
	// reference them and Kueue handles an empty flavor fine.
	return ctrl.Result{}, nil
}

// statusChanged ignores updateTime so idle clusters do not write every event.
func statusChanged(a, b v1.InstanceTypeStatus) bool {
	return a.Total != b.Total || a.Free != b.Free || !reflect.DeepEqual(a.Nodes, b.Nodes)
}

func resourceFlavor(c instancetype.Class) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		naming.KeyMetadata: map[string]any{
			naming.KeyName: naming.ResourceFlavor(c.Name),
			naming.KeyLabels: map[string]any{
				naming.LabelManagedBy: naming.ManagedBy, naming.LabelGPUClass: c.Name,
			},
		},
		naming.KeySpec: map[string]any{
			"nodeLabels": map[string]any{naming.LabelGPUProduct: c.Product},
		},
	}}
	u.SetGroupVersionKind(kf.ResourceFlavorGVK)
	return u
}

// scheduled passes pods bound to a node: any pod uses node CPU and memory.
func scheduled(o client.Object) bool {
	p, ok := o.(*corev1.Pod)
	return ok && p.Spec.NodeName != ""
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstanceTypeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	all := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{instanceTypeKey}
	})
	nodeChanged := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld.(*corev1.Node), e.ObjectNew.(*corev1.Node)
			return !maps.Equal(o.Labels, n.Labels) || o.Spec.Unschedulable != n.Spec.Unschedulable ||
				!equality.Semantic.DeepEqual(o.Spec.Taints, n.Spec.Taints) ||
				!equality.Semantic.DeepEqual(o.Status.Allocatable, n.Status.Allocatable) ||
				nodeReady(o) != nodeReady(n)
		},
	}
	podChanged := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return scheduled(e.Object) },
		DeleteFunc: func(e event.DeleteEvent) bool { return scheduled(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld.(*corev1.Pod), e.ObjectNew.(*corev1.Pod)
			return o.Spec.NodeName != n.Spec.NodeName || o.Status.Phase != n.Status.Phase
		},
	}
	configChanged := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		r.Config.Invalidate()
		return []reconcile.Request{instanceTypeKey}
	})
	isPlatformConfig := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == platform.ConfigMapName
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("instancetype").
		Watches(&corev1.Node{}, all, builder.WithPredicates(nodeChanged)).
		Watches(&corev1.Pod{}, all, builder.WithPredicates(podChanged)).
		Watches(&v1.InstanceType{}, all, builder.WithPredicates(predicate.Funcs{
			// Undo manual edits and deletions.
			CreateFunc: func(event.CreateEvent) bool { return false },
		})).
		Watches(&corev1.ConfigMap{}, configChanged, builder.WithPredicates(isPlatformConfig)).
		Complete(r)
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
