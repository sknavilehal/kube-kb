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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kbv1alpha1 "github.com/sknavilehal/kube-kb/api/v1alpha1"
)

const (
	ingestorImage        = "localhost:5000/ingestor:latest"
	ingestorTTLSeconds   = int32(600)
	ingestorBackoffLimit = int32(3)
	ingestorEmbedModel   = "nomic-embed-text"
	requeueAfter         = 5 * time.Minute
)

// fetchLatestCommitSHA calls the GitHub REST API to retrieve the latest commit
// SHA on the configured branch. If kb.Spec.GitHubTokenSecret is set, the token
// is read from that Secret's "token" key and attached as a Bearer header.
func (r *KnowledgeBaseReconciler) fetchLatestCommitSHA(
	ctx context.Context,
	kb *kbv1alpha1.KnowledgeBase,
) (string, error) {
	logger := log.FromContext(ctx)

	owner, repo, err := parseGitHubRepo(kb.Spec.GitHubRepo)
	if err != nil {
		return "", fmt.Errorf("parsing githubRepo: %w", err)
	}

	branch := kb.Spec.GitHubBranch
	if branch == "" {
		branch = "main"
	}

	apiURL := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/commits/%s",
		owner, repo, branch,
	)
	logger.Info("Fetching latest commit", "url", apiURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	// application/vnd.github.sha returns the bare 40-char SHA as the body.
	req.Header.Set("Accept", "application/vnd.github.sha")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	if kb.Spec.GitHubTokenSecret != "" {
		token, tokenErr := r.readSecretKey(ctx, kb.Namespace, kb.Spec.GitHubTokenSecret, "token")
		if tokenErr != nil {
			return "", fmt.Errorf("reading github token secret: %w", tokenErr)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github API returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(body))

	// Fallback: some GitHub API versions return JSON even with the SHA header.
	if strings.HasPrefix(sha, "{") {
		var payload struct {
			SHA string `json:"sha"`
		}
		if jsonErr := json.Unmarshal(body, &payload); jsonErr == nil {
			sha = payload.SHA
		}
	}

	if len(sha) < 7 {
		return "", fmt.Errorf("unexpected SHA from github API: %q", sha)
	}
	return sha, nil
}

// parseGitHubRepo extracts owner and repo from a GitHub HTTPS or SSH URL.
// Accepts: https://github.com/owner/repo or https://github.com/owner/repo.git
func parseGitHubRepo(repoURL string) (owner, repo string, err error) {
	re := regexp.MustCompile(`github\.com[/:]([^/]+)/([^/]+?)(?:\.git)?$`)
	m := re.FindStringSubmatch(repoURL)
	if len(m) != 3 {
		return "", "", fmt.Errorf("cannot parse GitHub owner/repo from %q", repoURL)
	}
	return m[1], m[2], nil
}

// readSecretKey fetches a single key's value from a Kubernetes Secret.
func (r *KnowledgeBaseReconciler) readSecretKey(
	ctx context.Context,
	namespace, secretName, key string,
) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		return "", err
	}
	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", namespace, secretName, key)
	}
	return string(val), nil
}

// ingestorJobName returns a deterministic Job name encoding the first 8 chars
// of the commit SHA so each new commit gets a distinct Job.
func ingestorJobName(kb *kbv1alpha1.KnowledgeBase, sha string) string {
	short := sha
	if len(sha) > 8 {
		short = sha[:8]
	}
	return kb.Name + "-ingest-" + short
}

// buildIngestorJob constructs the batchv1.Job that runs the Python ingestor
// container against the given commit SHA.
func (r *KnowledgeBaseReconciler) buildIngestorJob(
	kb *kbv1alpha1.KnowledgeBase,
	commitSHA string,
) *batchv1.Job {
	ttl := ingestorTTLSeconds
	backoff := ingestorBackoffLimit
	ollamaURL := fmt.Sprintf("http://%s-is-svc:11434", kb.Name)

	env := []corev1.EnvVar{
		{Name: "REPO_URL", Value: kb.Spec.GitHubRepo},
		{Name: "OLLAMA_BASE_URL", Value: ollamaURL},
		{Name: "DB_HOST", Value: kb.Name + "-vdb-svc"},
		{Name: "DB_USERNAME", Value: pgvectorProvider},
		{Name: "DB_NAME", Value: "vectordb"},
		{Name: "DB_COLLECTION", Value: kb.Name + "-docs"},
		{Name: "EMBED_MODEL", Value: ingestorEmbedModel},
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

	if kb.Spec.GitHubTokenSecret != "" {
		env = append(env, corev1.EnvVar{
			Name: "GITHUB_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: kb.Spec.GitHubTokenSecret,
					},
					Key: "token",
				},
			},
		})
	}

	shortSHA := commitSHA
	if len(commitSHA) > 8 {
		shortSHA = commitSHA[:8]
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingestorJobName(kb, commitSHA),
			Namespace: kb.Namespace,
			Labels: map[string]string{
				labelApp:              kb.Name + "-ingest",
				"kb.kubekb.io/commit": shortSHA,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{labelApp: kb.Name + "-ingest"},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:  "ingestor",
							Image: ingestorImage,
							Env:   env,
						},
					},
				},
			},
		},
	}
}

// reconcileIngestion fetches the latest commit SHA from GitHub, compares it to
// the observed commit in status, and creates a fresh ingestor Job whenever a
// change is detected (including the very first reconcile when status is empty).
func (r *KnowledgeBaseReconciler) reconcileIngestion(
	ctx context.Context,
	kb *kbv1alpha1.KnowledgeBase,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	isReady, err := r.deploymentReady(ctx, kb.Namespace, kb.Name+"-is-dep")
	if err != nil {
		return ctrl.Result{}, err
	} else if !isReady {
		logger.Info("InferenceServer not ready — skipping ingestion")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	vdbReady, err := r.deploymentReady(ctx, kb.Namespace, kb.Name+"-vdb-dep")
	if err != nil {
		return ctrl.Result{}, err
	} else if !vdbReady {
		logger.Info("VectorDB not ready — skipping ingestion")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	if kb.Spec.GitHubRepo == "" {
		logger.Info("No githubRepo configured — skipping ingestion")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// 1. Fetch latest commit SHA from GitHub.
	latestSHA, err := r.fetchLatestCommitSHA(ctx, kb)
	if err != nil {
		logger.Error(err, "Failed to fetch latest commit SHA")
		return ctrl.Result{}, err
	}
	logger.Info("Commit SHA check", "latest", latestSHA, "observed", kb.Status.ObservedGitCommit)

	// 2. Nothing changed — just requeue for the next polling interval.
	if latestSHA == kb.Status.ObservedGitCommit {
		logger.Info("No commit change — skipping ingestion")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// 3. New commit (or first run): delete any in-flight Jobs for this KB,
	//    then create a fresh Job for the latest SHA.
	if err := r.deleteExistingIngestorJobs(ctx, kb); err != nil {
		return ctrl.Result{}, err
	}

	job := r.buildIngestorJob(kb, latestSHA)
	if err := ctrl.SetControllerReference(kb, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	logger.Info("Created ingestor Job", "job", job.Name, "sha", latestSHA)

	// 4. Patch status: record the commit being ingested and set phase.
	patch := kb.DeepCopy()
	patch.Status.Phase = kbv1alpha1.PhaseIngesting
	patch.Status.ObservedGitCommit = latestSHA
	if err := r.Status().Patch(ctx, patch, client.MergeFrom(kb)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// deleteExistingIngestorJobs deletes all Jobs (in any state) owned by this
// KnowledgeBase that carry the ingest app label, cancelling any in-progress run.
func (r *KnowledgeBaseReconciler) deleteExistingIngestorJobs(
	ctx context.Context,
	kb *kbv1alpha1.KnowledgeBase,
) error {
	logger := log.FromContext(ctx)

	jobList := &batchv1.JobList{}
	if err := r.List(ctx, jobList,
		client.InNamespace(kb.Namespace),
		client.MatchingLabels{labelApp: kb.Name + "-ingest"},
	); err != nil {
		return err
	}

	propagation := metav1.DeletePropagationForeground
	for i := range jobList.Items {
		job := &jobList.Items[i]
		logger.Info("Deleting stale ingestor Job", "job", job.Name)
		if err := r.Delete(ctx, job, &client.DeleteOptions{
			PropagationPolicy: &propagation,
		}); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }
