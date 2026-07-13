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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.4/pkg/reconcile
func (r *KnowledgeBaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("knowledgebase", req.NamespacedName)
	logger.Info("Hello from KubeKB!", "resource", req.NamespacedName)

	kb := kbv1alpha1.KnowledgeBase{}
	if err := r.Get(ctx, req.NamespacedName, &kb); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("KnowledgeBase resource not found. Ignoring.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get KnowledgeBase")
		return ctrl.Result{}, err
	}

	logger.Info("=== Hello World: Reconciling KnowledgeBase ===")
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

	return ctrl.Result{}, nil
}

// --- Inference Server ---

func (r *KnowledgeBaseReconciler) buildISPVC(kb *kbv1alpha1.KnowledgeBase) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-is-pvc",
			Namespace: kb.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
		},
	}
}

func (r *KnowledgeBaseReconciler) buildISService(kb *kbv1alpha1.KnowledgeBase) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-is-svc",
			Namespace: kb.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": kb.Name + "-is",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       11434,
					TargetPort: intstr.FromInt(11434),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func (r *KnowledgeBaseReconciler) buildISDeployment(kb *kbv1alpha1.KnowledgeBase) *appsv1.Deployment {
	labels := map[string]string{"app": kb.Name + "-is"}
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-is-dep",
			Namespace: kb.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:  "ollama-init",
							Image: "ollama/ollama:latest",
						Command: []string{
							"sh", "-c",
							"ollama pull " + kb.Spec.InferenceServer.Model,
						},
					},
				},
				Containers: []corev1.Container{
						{
							Name:  "ollama",
							Image: "ollama/ollama:latest",
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 11434,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "model-cache",
									MountPath: "/root/.ollama",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "model-cache",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: kb.Name + "-is-pvc",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *KnowledgeBaseReconciler) reconcileInferenceServer(ctx context.Context, kb *kbv1alpha1.KnowledgeBase) error {
	pvc := r.buildISPVC(kb)
	svc := r.buildISService(kb)
	dep := r.buildISDeployment(kb)

	for _, obj := range []client.Object{pvc, svc, dep} {
		if err := ctrl.SetControllerReference(kb, obj, r.Scheme); err != nil {
			return err
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
			return nil
		}); err != nil {
			return err
		}
	}

	return nil
}

// --- Vector DB (pgvector) ---

func (r *KnowledgeBaseReconciler) buildVDBPVC(kb *kbv1alpha1.KnowledgeBase) *corev1.PersistentVolumeClaim {
	storage := kb.Spec.VectorDB.Storage
	if storage == "" {
		storage = "10Gi"
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-vdb-pvc",
			Namespace: kb.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storage),
				},
			},
		},
	}
}

func (r *KnowledgeBaseReconciler) buildVDBDeployment(kb *kbv1alpha1.KnowledgeBase) *appsv1.Deployment {
	labels := map[string]string{"app": kb.Name + "-vdb"}
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-vdb-dep",
			Namespace: kb.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "pgvector",
							Image: "pgvector/pgvector:pg16",
							Ports: []corev1.ContainerPort{
								{
									Name:          "postgres",
									ContainerPort: 5432,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								{
									Name:  "POSTGRES_DB",
									Value: "vectordb",
								},
								{
									Name:  "POSTGRES_USER",
									Value: "pgvector",
								},
								{
									Name:  "POSTGRES_PASSWORD",
									Value: "pgvector", // TODO: source from a Secret
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "pgdata",
									MountPath: "/var/lib/postgresql/data",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "pgdata",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: kb.Name + "-vdb-pvc",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *KnowledgeBaseReconciler) buildVDBService(kb *kbv1alpha1.KnowledgeBase) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-vdb-svc",
			Namespace: kb.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": kb.Name + "-vdb",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "postgres",
					Port:       5432,
					TargetPort: intstr.FromInt(5432),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func (r *KnowledgeBaseReconciler) reconcileVectorDB(ctx context.Context, kb *kbv1alpha1.KnowledgeBase) error {
	// Managed providers (e.g. aws-rds) need no in-cluster resources.
	if kb.Spec.VectorDB.Provider != "pgvector" {
		return nil
	}

	pvc := r.buildVDBPVC(kb)
	dep := r.buildVDBDeployment(kb)
	svc := r.buildVDBService(kb)

	for _, obj := range []client.Object{pvc, dep, svc} {
		if err := ctrl.SetControllerReference(kb, obj, r.Scheme); err != nil {
			return err
		}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
			return nil
		}); err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KnowledgeBaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kbv1alpha1.KnowledgeBase{}).
		Complete(r)
}
