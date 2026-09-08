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

// Package controller implements the KnowledgeBase reconciliation logic.
package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kbv1alpha1 "github.com/sknavilehal/kube-kb/api/v1alpha1"
)

// KnowledgeBaseReconciler reconciles a KnowledgeBase object
type KnowledgeBaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kb.kubekb.io,resources=knowledgebases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kb.kubekb.io,resources=knowledgebases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kb.kubekb.io,resources=knowledgebases/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

const (
	labelApp         = "app"
	pgvectorProvider = "pgvector"
)

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.4/pkg/reconcile
func (r *KnowledgeBaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("knowledgebase", req.NamespacedName)
	logger.Info("Reconciling KnowledgeBase", "resource", req.NamespacedName)

	kb := kbv1alpha1.KnowledgeBase{}
	if err := r.Get(ctx, req.NamespacedName, &kb); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("KnowledgeBase resource not found. Ignoring.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get KnowledgeBase")
		return ctrl.Result{}, err
	}

	logger.Info("Metadata", "name", kb.Name, "namespace", kb.Namespace, "creationTimestamp", kb.CreationTimestamp)
	logger.Info("Spec",
		"inferenceServer.provider", kb.Spec.InferenceServer.Provider,
		"inferenceServer.model", kb.Spec.InferenceServer.Model,
		"vectorDB.provider", kb.Spec.VectorDB.Provider,
		"githubRepo", kb.Spec.GitHubRepo,
		"githubBranch", kb.Spec.GitHubBranch,
	)
	logger.Info("Status",
		"phase", kb.Status.Phase,
		"observedGitCommit", kb.Status.ObservedGitCommit,
		"conditionsCount", len(kb.Status.Conditions),
	)

	if err := r.reconcileInferenceServer(ctx, &kb); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileVectorDB(ctx, &kb); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileRetriever(ctx, &kb); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updatePhase(ctx, &kb); err != nil {
		return ctrl.Result{}, err
	}

	return r.reconcileIngestion(ctx, &kb)
}

// updatePhase derives the overall phase from the readiness of the managed
// Deployments and persists it to status. The retriever Deployment may not exist
// on the first reconcile, so a missing resource is treated as not-ready.
func (r *KnowledgeBaseReconciler) updatePhase(ctx context.Context, kb *kbv1alpha1.KnowledgeBase) error {
	isReady, err := r.deploymentReady(ctx, kb.Namespace, kb.Name+"-is-dep")
	if err != nil {
		return err
	}
	vdbReady, err := r.deploymentReady(ctx, kb.Namespace, kb.Name+"-vdb-dep")
	if err != nil {
		return err
	}
	retReady, err := r.deploymentReady(ctx, kb.Namespace, kb.Name+"-retriever-dep")
	if err != nil {
		return err
	}

	original := kb.DeepCopy()
	if isReady && vdbReady && retReady {
		kb.Status.Phase = kbv1alpha1.PhaseReady
	} else {
		kb.Status.Phase = kbv1alpha1.PhasePending
	}

	return r.Status().Patch(ctx, kb, client.MergeFrom(original))
}

// SetupWithManager sets up the controller with the Manager.
func (r *KnowledgeBaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kbv1alpha1.KnowledgeBase{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
