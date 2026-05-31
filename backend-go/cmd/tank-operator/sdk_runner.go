package main

import corev1 "k8s.io/api/core/v1"

func podHasSDKRunner(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == "agent-runner" || c.Name == "codex-runner" || c.Name == "gemini-runner" {
			return true
		}
	}
	return false
}
