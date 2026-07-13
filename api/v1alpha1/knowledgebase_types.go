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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KnowledgeBaseSpec defines the desired state of KnowledgeBase
type KnowledgeBaseSpec struct {
	InferenceServer InferenceServerSpec `json:"inferenceServer,omitempty"`
	VectorDB        VectorDBSpec        `json:"vectorDB,omitempty"`
	GitHubRepo      string              `json:"githubRepo,omitempty"`
	GitHubBranch    string              `json:"githubBranch,omitempty"`
}

type InferenceServerSpec struct {
	// +kubebuilder:validation:Enum=ollama;openai;anthropic
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type VectorDBSpec struct {
	// +kubebuilder:validation:Enum=pgvector;aws-rds
	Provider string `json:"provider,omitempty"`
	Storage  string `json:"storage,omitempty"`
}

// KnowledgeBaseStatus defines the observed state of KnowledgeBase
type KnowledgeBaseStatus struct {
	Phase             KnowledgeBasePhase `json:"phase,omitempty"`
	ObservedGitCommit string             `json:"observedGitCommit,omitempty"`

	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type KnowledgeBasePhase string

const (
	PhasePending   KnowledgeBasePhase = "Pending"
	PhaseReady     KnowledgeBasePhase = "Ready"
	PhaseIngesting KnowledgeBasePhase = "Ingesting"
	PhaseFailed    KnowledgeBasePhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KnowledgeBase is the Schema for the knowledgebases API
type KnowledgeBase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KnowledgeBaseSpec   `json:"spec,omitempty"`
	Status KnowledgeBaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KnowledgeBaseList contains a list of KnowledgeBase
type KnowledgeBaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KnowledgeBase `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KnowledgeBase{}, &KnowledgeBaseList{})
}
