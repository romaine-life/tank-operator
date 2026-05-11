package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// K8sSubject holds a validated ServiceAccount caller identity.
type K8sSubject struct {
	Namespace string
	Name      string
}

func (s K8sSubject) Qualified() string {
	return s.Namespace + "/" + s.Name
}

// ValidateSAToken validates a Kubernetes ServiceAccount bearer token via
// TokenReview and returns the caller's namespace/name.
func ValidateSAToken(ctx context.Context, k8s kubernetes.Interface, token string, audiences []string) (K8sSubject, error) {
	tr := &authv1.TokenReview{
		Spec: authv1.TokenReviewSpec{
			Token:     token,
			Audiences: audiences,
		},
	}
	result, err := k8s.AuthenticationV1().TokenReviews().Create(ctx, tr, metav1.CreateOptions{})
	if err != nil {
		return K8sSubject{}, fmt.Errorf("TokenReview failed: %w", err)
	}
	if !result.Status.Authenticated {
		return K8sSubject{}, errHTTP{status: http.StatusUnauthorized, message: "ServiceAccount token not authenticated"}
	}
	username := result.Status.User.Username
	// Format: system:serviceaccount:<namespace>:<name>
	parts := strings.Split(username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" {
		return K8sSubject{}, errHTTP{status: http.StatusUnauthorized, message: "unexpected token subject: " + username}
	}
	return K8sSubject{Namespace: parts[2], Name: parts[3]}, nil
}

// ParseSAToken extracts Bearer token from Authorization header.
func ParseSAToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return "", errHTTP{status: http.StatusUnauthorized, message: "missing Authorization header"}
	}
	return strings.TrimSpace(auth[7:]), nil
}

// K8sAuthSubjectMap parses "ns/name=email,..." into a lookup map.
func K8sAuthSubjectMap(raw string) map[string]string {
	m := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			continue
		}
		subj := strings.TrimSpace(entry[:idx])
		email := strings.ToLower(strings.TrimSpace(entry[idx+1:]))
		if subj != "" && email != "" {
			m[subj] = email
		}
	}
	return m
}
