/*
Copyright 2026 Red Hat, Inc.

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

	ocstlsv1 "github.com/red-hat-storage/ocs-tls-profiles/api/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	TLSProfileName = "ocs-tls-profile"
)

// TLSProfileReconciler restarts the container when the TLSProfile changes so
// the new TLS settings are applied to all servers on the next startup.
type TLSProfileReconciler struct {
	client.Client
	ShutdownContainer func()
	// InitialGeneration is the generation of the TLSProfile at operator startup
	// (0 if it did not exist). The reconciler skips shutdown when the observed
	// generation matches this value so that the informer's initial sync event
	// does not trigger an unnecessary restart.
	InitialGeneration int64
	Namespace         string
}

// +kubebuilder:rbac:groups=ocs.openshift.io,resources=tlsprofiles,verbs=get;list;watch

func (r *TLSProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	profile := &ocstlsv1.TLSProfile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		if kerrors.IsNotFound(err) {
			if r.InitialGeneration != 0 {
				log.Info("TLSProfile deleted, restarting to apply default TLS configuration")
				r.ShutdownContainer()
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get TLSProfile: %w", err)
	}

	if profile.Generation == r.InitialGeneration {
		return ctrl.Result{}, nil
	}

	log.Info("TLSProfile changed, restarting to apply new TLS configuration")
	r.ShutdownContainer()
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *TLSProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(
			&ocstlsv1.TLSProfile{},
			builder.WithPredicates(
				predicate.NewPredicateFuncs(func(obj client.Object) bool {
					return obj.GetName() == TLSProfileName &&
						obj.GetNamespace() == r.Namespace
				}),
				predicate.GenerationChangedPredicate{},
			),
		).
		Complete(r)
}
