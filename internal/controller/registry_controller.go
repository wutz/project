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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/wutz/project/api/v1alpha1"
	"github.com/wutz/project/pkg/harbor"
	"github.com/wutz/project/pkg/naming"
	"github.com/wutz/project/pkg/platform"
)

const (
	registryFinalizer = "forgeai.wutz.dev/registry"
	kindSecret        = "Secret"
	// Keys of the registry Secret besides .dockerconfigjson.
	RegistryKeyUsername = "username"
	RegistryKeyPassword = "password"
	RegistryKeyServer   = "server"
)

// HarborFactory builds an admin Harbor client; replaced in tests.
type HarborFactory func(reg platform.Registry, username, password string) *harbor.Client

// RegistryReconciler gives every Project a Harbor project named after its
// namespace and a robot account stored as a pull secret in the namespace.
type RegistryReconciler struct {
	client.Client
	APIReader client.Reader
	Config    *platform.Loader
	// ConfigNamespace holds the Harbor admin Secret.
	ConfigNamespace string
	NewHarbor       HarborFactory
}

// +kubebuilder:rbac:groups=project.wutz.dev,resources=projects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=project.wutz.dev,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets;serviceaccounts,verbs=get;list;watch;create;update;patch;delete

func (r *RegistryReconciler) harbor(ctx context.Context, cfg *platform.Config) (*harbor.Client, error) {
	s := &corev1.Secret{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: r.ConfigNamespace, Name: cfg.Registry.AdminSecret}, s); err != nil {
		return nil, fmt.Errorf("harbor admin secret %s/%s: %w", r.ConfigNamespace, cfg.Registry.AdminSecret, err)
	}
	f := r.NewHarbor
	if f == nil {
		f = func(reg platform.Registry, u, p string) *harbor.Client {
			return harbor.New(reg.APIURL(), u, p, reg.Insecure)
		}
	}
	return f(cfg.Registry, string(s.Data["username"]), string(s.Data["password"])), nil
}

func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	p := &v1.Project{}
	if err := r.Get(ctx, req.NamespacedName, p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	cfg, err := r.Config.Get(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !cfg.Registry.Enabled() {
		return ctrl.Result{}, nil
	}
	ns := naming.Namespace(cfg.NamespacePrefix, p.Name)

	if !p.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(p, registryFinalizer) {
			return ctrl.Result{}, nil
		}
		if p.Spec.DeletionPolicy == v1.DeletionDelete {
			h, err := r.harbor(ctx, cfg)
			if err != nil {
				return ctrl.Result{}, err
			}
			if err := h.DeleteProject(ctx, ns); err != nil {
				return ctrl.Result{}, fmt.Errorf("delete harbor project %s: %w", ns, err)
			}
			log.Info("Deleted Harbor project", "project", ns)
		}
		controllerutil.RemoveFinalizer(p, registryFinalizer)
		return ctrl.Result{}, r.Update(ctx, p)
	}
	// Wait for the namespace.
	if !meta.IsStatusConditionTrue(p.Status.Conditions, v1.ConditionReady) {
		return ctrl.Result{}, nil
	}
	if controllerutil.AddFinalizer(p, registryFinalizer) {
		if err := r.Update(ctx, p); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := p.DeepCopy()
	status := v1.RegistryStatus{Project: ns, URL: cfg.Registry.Domain + "/" + ns, Secret: naming.RegistrySecretName}
	if err := r.reconcile(ctx, p, cfg, ns); err != nil {
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type: v1.ConditionRegistry, Status: metav1.ConditionFalse, Reason: "HarborError",
			Message: err.Error(), ObservedGeneration: p.Generation,
		})
		_ = r.Status().Patch(ctx, p, statusPatch(orig))
		log.Error(err, "Failed to reconcile Harbor project", "project", ns)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	p.Status.Registry = status
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: v1.ConditionRegistry, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: p.Generation,
	})
	return ctrl.Result{}, r.Status().Patch(ctx, p, statusPatch(orig))
}

func (r *RegistryReconciler) reconcile(ctx context.Context, p *v1.Project, cfg *platform.Config, ns string) error {
	h, err := r.harbor(ctx, cfg)
	if err != nil {
		return err
	}
	hp, err := h.EnsureProject(ctx, ns)
	if err != nil {
		return err
	}
	limit := int64(-1)
	if q := p.Spec.Quota.RegistryStorage; q != nil {
		limit = q.Value()
	}
	if err := h.SetStorageQuota(ctx, hp, limit); err != nil {
		return err
	}

	// Robot secrets are only known on creation, so the Secret is the source
	// of truth; a missing or incomplete Secret resets the robot secret.
	cur := &corev1.Secret{}
	err = r.APIReader.Get(ctx, types.NamespacedName{Namespace: ns, Name: naming.RegistrySecretName}, cur)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	username, password := string(cur.Data[RegistryKeyUsername]), string(cur.Data[RegistryKeyPassword])
	// Harbor can lose the robot while the Secret stays (the robot or the
	// project was removed in Harbor), so the Secret alone does not prove the
	// login works. A missing robot is issued again with a new secret.
	exists, err := h.HasRobot(ctx, hp, harbor.RobotName)
	if err != nil {
		return err
	}
	if !exists || password == "" || string(cur.Data[RegistryKeyServer]) != cfg.Registry.Domain {
		if password, err = harbor.GenerateSecret(); err != nil {
			return err
		}
		if username, err = h.EnsureRobot(ctx, hp, harbor.RobotName, password); err != nil {
			return err
		}
		logf.FromContext(ctx).Info("Issued Harbor robot credentials", "project", ns, "robot", username)
	}
	dcj, err := harbor.DockerConfigJSON(cfg.Registry.Domain, username, password)
	if err != nil {
		return err
	}
	if err := apply(ctx, r.Client, &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: kindSecret},
		ObjectMeta: metav1.ObjectMeta{
			Name: naming.RegistrySecretName, Namespace: ns, Labels: projectLabels(p),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: dcj,
			RegistryKeyUsername:        []byte(username),
			RegistryKeyPassword:        []byte(password),
			RegistryKeyServer:          []byte(cfg.Registry.Domain),
		},
	}); err != nil {
		return err
	}
	// Pods without explicit pull secrets (e.g. kubectl-created ones) pull
	// from the project registry too.
	return apply(ctx, r.Client, &corev1.ServiceAccount{
		TypeMeta:         metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta:       metav1.ObjectMeta{Name: "default", Namespace: ns},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: naming.RegistrySecretName}},
	})
}

// readyChanged passes Project updates that flip the Ready condition, so the
// registry is set up as soon as the namespace exists.
func readyChanged(e event.UpdateEvent) bool {
	o, ok1 := e.ObjectOld.(*v1.Project)
	n, ok2 := e.ObjectNew.(*v1.Project)
	return ok1 && ok2 && meta.IsStatusConditionTrue(o.Status.Conditions, v1.ConditionReady) !=
		meta.IsStatusConditionTrue(n.Status.Conditions, v1.ConditionReady)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	bySecret := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if p := o.GetLabels()[naming.LabelProject]; p != "" && o.GetName() == naming.RegistrySecretName {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: p}}}
		}
		return nil
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Project{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.Funcs{UpdateFunc: readyChanged},
		))).
		// Recreate the robot secret when someone deletes it.
		Watches(&corev1.Secret{}, bySecret, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(event.CreateEvent) bool { return false },
			UpdateFunc: func(event.UpdateEvent) bool { return false },
		})).
		Named("registry").
		Complete(r)
}
