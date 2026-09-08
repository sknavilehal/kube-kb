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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kbv1alpha1 "github.com/sknavilehal/kube-kb/api/v1alpha1"
)

const (
	retrieverImage      = "localhost:5000/retriever:latest"
	retrieverEmbedModel = "nomic-embed-text"
	retrieverPort       = int32(8000)
)

func (r *KnowledgeBaseReconciler) buildRetrieverDeployment(kb *kbv1alpha1.KnowledgeBase) *appsv1.Deployment {
	labels := map[string]string{labelApp: kb.Name + "-retriever"}
	replicas := int32(1)
	ollamaURL := fmt.Sprintf("http://%s-is-svc:11434", kb.Name)

	env := []corev1.EnvVar{
		{Name: "DB_HOST", Value: kb.Name + "-vdb-svc"},
		{Name: "DB_USERNAME", Value: pgvectorProvider},
		{Name: "DB_NAME", Value: "vectordb"},
		{Name: "DB_COLLECTION", Value: kb.Name + "-docs"},
		{Name: "OLLAMA_BASE_URL", Value: ollamaURL},
		{Name: "EMBED_MODEL", Value: retrieverEmbedModel},
		{Name: "LLM_MODEL", Value: kb.Spec.InferenceServer.Model},
		{
			Name: "DB_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: kb.Name + "-vdb-secret",
					},
					Key:      "password",
					Optional: boolPtr(true),
				},
			},
		},
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-retriever-dep",
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
							Name:  "retriever",
							Image: retrieverImage,
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: retrieverPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: env,
						},
					},
				},
			},
		},
	}
}

func (r *KnowledgeBaseReconciler) buildRetrieverService(kb *kbv1alpha1.KnowledgeBase) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kb.Name + "-retriever-svc",
			Namespace: kb.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				labelApp: kb.Name + "-retriever",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       retrieverPort,
					TargetPort: intstr.FromInt32(retrieverPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
}

func (r *KnowledgeBaseReconciler) reconcileRetriever(ctx context.Context, kb *kbv1alpha1.KnowledgeBase) error {
	dep := r.buildRetrieverDeployment(kb)
	svc := r.buildRetrieverService(kb)

	for _, obj := range []client.Object{dep, svc} {
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
