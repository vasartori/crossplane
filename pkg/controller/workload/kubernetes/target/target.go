/*
Copyright 2019 The Crossplane Authors.

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

package target

import (
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	util "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	runtimev1alpha1 "github.com/crossplaneio/crossplane-runtime/apis/core/v1alpha1"
	"github.com/crossplaneio/crossplane-runtime/pkg/logging"
	"github.com/crossplaneio/crossplane-runtime/pkg/meta"
	"github.com/crossplaneio/crossplane-runtime/pkg/resource"
	computev1alpha1 "github.com/crossplaneio/crossplane/apis/compute/v1alpha1"
	workloadv1alpha1 "github.com/crossplaneio/crossplane/apis/workload/v1alpha1"
)

const (
	reconcileTimeout = 1 * time.Minute

	errGetKubernetesCluster = "unable to get KubernetesCluster"
	errCreateOrUpdateTarget = "unable to create or update KubernetesTarget"
	errTargetConflict       = "cannot establish control of existing KubernetesTarget"
)

func clusterIsBound(obj runtime.Object) bool {
	r, ok := obj.(*computev1alpha1.KubernetesCluster)
	if !ok {
		return false
	}

	return r.GetBindingPhase() == runtimev1alpha1.BindingPhaseBound
}

// Setup adds a controller that creates KubernetesTargets for
// KubernetesClusters.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := "autotarget/" + strings.ToLower(computev1alpha1.KubernetesClusterKind)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&computev1alpha1.KubernetesCluster{}).
		WithEventFilter(resource.NewPredicates(clusterIsBound)).
		Complete(&Reconciler{
			kube: mgr.GetClient(),
			log:  l.WithValues("controller", name)})
}

// A Reconciler creates KubernetesTargets for KubernetesClusters.
type Reconciler struct {
	kube client.Client
	log  logging.Logger
}

// Reconcile attempts to create a KubernetesTarget for a KubernetesCluster.
func (r *Reconciler) Reconcile(req reconcile.Request) (reconcile.Result, error) {
	r.log.Debug("Reconciling", "request", req)

	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	cluster := &computev1alpha1.KubernetesCluster{}
	if err := r.kube.Get(ctx, req.NamespacedName, cluster); err != nil {
		return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(err), errGetKubernetesCluster)
	}

	// This KubernetesCluster has been deleted. The KubernetesTarget will be
	// cleaned up by garbage collection.
	if meta.WasDeleted(cluster) {
		return reconcile.Result{Requeue: false}, nil
	}

	target := &workloadv1alpha1.KubernetesTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name:            string(cluster.GetUID()),
			Namespace:       cluster.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{meta.AsController(meta.ReferenceTo(cluster, computev1alpha1.KubernetesClusterGroupVersionKind))},
		},
	}

	_, err := util.CreateOrUpdate(ctx, r.kube, target, func() error {
		if c := metav1.GetControllerOf(target); c == nil || c.UID != cluster.GetUID() {
			return errors.New(errTargetConflict)
		}

		// The Target secret reference is set to match the KubernetesCluster and
		// all labels are propagated. Subsequent updates to the secret reference
		// or labels of the KubernetesCluster will also be propagated to the
		// Target.
		target.SetWriteConnectionSecretToReference(cluster.GetWriteConnectionSecretToReference())
		target.SetLabels(cluster.GetLabels())

		return nil
	})

	return reconcile.Result{}, errors.Wrap(err, errCreateOrUpdateTarget)
}
