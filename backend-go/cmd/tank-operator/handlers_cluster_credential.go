package main

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// clusterCredentialTokenTTLSeconds is the lifetime of a minted trusted-SA
// token. kubectl's exec credential plugin caches by expirationTimestamp and
// re-invokes the plugin (which re-mints) when it lapses.
const clusterCredentialTokenTTLSeconds int64 = 3600

// trustedSessionServiceAccount returns the name of the cluster-admin SA whose
// tokens back kubectl for non-restricted sessions. It is never a pod's own
// serviceAccountName — only minted on demand here — so MCP auth (bound to the
// base session SA) and the read-only posture of restricted sessions are
// unaffected. See k8s/templates/rbac.yaml.
func (s *appServer) trustedSessionServiceAccount() string {
	return s.sessionServiceAccount + "-trusted"
}

// handleInternalClusterCredential mints a short-lived token for the trusted
// (cluster-admin) session SA, for use as a kubectl credential. It is the
// on-demand source behind the exec-credential kubeconfig installed in
// NON-RESTRICTED session pods, giving them full cluster write without changing
// the pod's identity.
//
// Auth is the calling pod's own audience-scoped SA token: requireInternalSession
// PodCaller TokenReviews it, matches the bound pod UID, and resolves the session
// in the active scope. Restricted sessions are refused, so a restricted/test
// pod cannot obtain cluster-admin even by calling this directly.
func (s *appServer) handleInternalClusterCredential(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireInternalSessionPodCaller(w, r)
	if !ok {
		return
	}

	pod, err := s.k8s.CoreV1().Pods(s.namespace).Get(r.Context(), caller.PodName, metav1.GetOptions{})
	if err != nil {
		writeError(w, http.StatusForbidden, "session pod not found")
		return
	}
	if podRestrictedGit(pod) {
		writeError(w, http.StatusForbidden, "cluster credential is not available to restricted-git sessions")
		return
	}

	trustedSA := s.trustedSessionServiceAccount()
	req := &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{
			ExpirationSeconds: ptrInt64(clusterCredentialTokenTTLSeconds),
		},
	}
	tok, err := s.k8s.CoreV1().ServiceAccounts(s.namespace).CreateToken(r.Context(), trustedSA, req, metav1.CreateOptions{})
	if err != nil {
		slog.Error("cluster credential mint failed", "session_id", caller.SessionID, "trusted_sa", trustedSA, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "failed to mint cluster credential")
		return
	}

	// kubectl client.authentication.k8s.io/v1 ExecCredential — returned verbatim
	// by the in-pod exec plugin.
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1",
		"kind":       "ExecCredential",
		"status": map[string]any{
			"token":               tok.Status.Token,
			"expirationTimestamp": tok.Status.ExpirationTimestamp.Time.UTC().Format(time.RFC3339),
		},
	})
}

// podRestrictedGit reports whether the pod runs in restricted-git mode, read
// from the TANK_RESTRICTED_GIT env the manifest sets on its containers.
func podRestrictedGit(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	containers := append([]corev1.Container{}, pod.Spec.Containers...)
	containers = append(containers, pod.Spec.InitContainers...)
	for _, c := range containers {
		for _, e := range c.Env {
			if e.Name != "TANK_RESTRICTED_GIT" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(e.Value)) {
			case "1", "true", "yes", "on":
				return true
			}
		}
	}
	return false
}

func ptrInt64(v int64) *int64 { return &v }
