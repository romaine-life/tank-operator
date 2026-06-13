package main

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPodHasSDKRunnerRecognizesSupportedRunnerContainers(t *testing.T) {
	tests := []struct {
		name       string
		containers []corev1.Container
		want       bool
	}{
		{
			name:       "claude agent runner",
			containers: []corev1.Container{{Name: "mcp-auth-proxy"}, {Name: "claude"}, {Name: "claude-runner"}},
			want:       true,
		},
		{
			name:       "codex runner",
			containers: []corev1.Container{{Name: "mcp-auth-proxy"}, {Name: "claude"}, {Name: "codex-runner"}},
			want:       true,
		},
		{
			name:       "terminal only",
			containers: []corev1.Container{{Name: "mcp-auth-proxy"}, {Name: "claude"}},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: tt.containers}}
			if got := podHasSDKRunner(pod); got != tt.want {
				t.Fatalf("podHasSDKRunner() = %v, want %v", got, tt.want)
			}
		})
	}
}
