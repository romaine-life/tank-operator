package main

import corev1 "k8s.io/api/core/v1"

func podHasSDKRunner(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == "claude-runner" || c.Name == "codex-runner" {
			return true
		}
	}
	return false
}
