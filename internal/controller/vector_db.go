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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kbv1alpha1 "github.com/sknavilehal/kube-kb/api/v1alpha1"
)

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
	labels := map[string]string{labelApp: kb.Name + "-vdb"}
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
							Name:  pgvectorProvider,
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
									Value: pgvectorProvider,
								},
								{
									Name:  "POSTGRES_PASSWORD",
									Value: pgvectorProvider, // TODO: source from a Secret
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
				labelApp: kb.Name + "-vdb",
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

func (r *KnowledgeBaseReconciler) buildVDBSecret(kb *kbv1alpha1.KnowledgeBase) *corev1.Secret {

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-vdb-secret",
			Namespace: kb.Namespace,
		},
		StringData: map[string]string{
			"password": pgvectorProvider, // TODO: generate a random password and store it in the Secret
		},
	}
}

func (r *KnowledgeBaseReconciler) reconcileVectorDB(ctx context.Context, kb *kbv1alpha1.KnowledgeBase) error {
	// Managed providers (e.g. aws-rds) need no in-cluster resources.
	if kb.Spec.VectorDB.Provider != pgvectorProvider {
		return nil
	}

	pvc := r.buildVDBPVC(kb)
	dep := r.buildVDBDeployment(kb)
	svc := r.buildVDBService(kb)
	secret := r.buildVDBSecret(kb)

	for _, obj := range []client.Object{pvc, dep, svc, secret} {
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
