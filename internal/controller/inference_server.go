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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kbv1alpha1 "github.com/sknavilehal/kube-kb/api/v1alpha1"
)

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
				labelApp: kb.Name + "-is",
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
	labels := map[string]string{labelApp: kb.Name + "-is"}
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
								"ollama serve & until ollama list > /dev/null 2>&1; do sleep 1; done && ollama pull " + kb.Spec.InferenceServer.Model + " && ollama pull nomic-embed-text",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "model-cache",
									MountPath: "/root/.ollama",
								},
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

// deploymentReady reports whether the Deployment with the given name has at
// least one ready replica. A NotFound error is treated as "not ready" rather
// than an error, so reconciles stay idempotent before the resource exists.
func (r *KnowledgeBaseReconciler) deploymentReady(ctx context.Context, namespace, name string) (bool, error) {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return dep.Status.ReadyReplicas >= 1, nil
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
