package compat

import (
	"strings"
	"testing"
)

func TestNormalizeSessionMode(t *testing.T) {
	tests := map[string]string{
		"":                      ClaudeCLIMode,
		"subscription":          ClaudeCLIMode,
		"subscription_headless": ClaudeGUIMode,
		"codex_subscription":    CodexCLIMode,
		"codex_headless":        CodexGUIMode,
		"pi_subscription":       PiCLIMode,
		"codex_config":          CodexConfigMode,
		"unknown":               "unknown",
	}
	for input, want := range tests {
		if got := NormalizeSessionMode(input); got != want {
			t.Fatalf("NormalizeSessionMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOwnerLabelMatchesPython(t *testing.T) {
	if got, want := OwnerLabel("nelson@romaine.life"), "u-db1458e0eb6e9e75"; got != want {
		t.Fatalf("OwnerLabel() = %q, want %q", got, want)
	}
	if got, want := OwnerLabel("User@Example.COM"), "u-857296a3c8a81182"; got != want {
		t.Fatalf("OwnerLabel() = %q, want %q", got, want)
	}
}

func TestNormalizeName(t *testing.T) {
	blank := " \t\n "
	if got := NormalizeName(&blank); got != nil {
		t.Fatalf("blank name normalized to %q, want nil", *got)
	}
	long := strings.Repeat("x", MaxNameLength+5)
	got := NormalizeName(&long)
	if got == nil {
		t.Fatal("long name normalized to nil")
	}
	if len(*got) != MaxNameLength {
		t.Fatalf("normalized name length = %d, want %d", len(*got), MaxNameLength)
	}
}

func TestRunIDsAndPaths(t *testing.T) {
	valid := []string{"abc123", "run_abc-123.4", strings.Repeat("a", 80)}
	for _, value := range valid {
		if !ValidateRunID(value) {
			t.Fatalf("ValidateRunID(%q) = false, want true", value)
		}
	}
	invalid := []string{"", strings.Repeat("a", 81), "../../bad", "has space"}
	for _, value := range invalid {
		if ValidateRunID(value) {
			t.Fatalf("ValidateRunID(%q) = true, want false", value)
		}
	}
	if got, want := RunStreamPath("abc"), "/tmp/tank-run-abc.stream"; got != want {
		t.Fatalf("RunStreamPath() = %q, want %q", got, want)
	}
	if got, want := RunPIDPath("abc"), "/tmp/tank-run-abc.pid"; got != want {
		t.Fatalf("RunPIDPath() = %q, want %q", got, want)
	}
}

func TestDocumentIDsAndShapes(t *testing.T) {
	if got, want := SessionDocID("default", "12"), "session:12"; got != want {
		t.Fatalf("SessionDocID(default) = %q, want %q", got, want)
	}
	if got, want := SessionDocID("slot-a", "12"), "session:slot-a:12"; got != want {
		t.Fatalf("SessionDocID(slot) = %q, want %q", got, want)
	}
	if got, want := SessionCounterDocID("default"), "session-counter"; got != want {
		t.Fatalf("SessionCounterDocID(default) = %q, want %q", got, want)
	}
	if got, want := SessionCounterDocID("slot-a"), "session-counter:slot-a"; got != want {
		t.Fatalf("SessionCounterDocID(slot) = %q, want %q", got, want)
	}

	sessionDoc := SessionDoc(SessionRecord{
		ID:      "12",
		Email:   "USER@example.COM",
		Mode:    ClaudeCLIMode,
		Scope:   "default",
		PodName: "session-12",
		Visible: true,
	})
	if got, want := sessionDoc["id"], "session:12"; got != want {
		t.Fatalf("session doc id = %v, want %q", got, want)
	}
	if got, want := sessionDoc["email"], "user@example.com"; got != want {
		t.Fatalf("session doc email = %v, want %q", got, want)
	}

	runDoc := ActiveRunDoc(ActiveRunRecord{
		SessionID:  "12",
		Email:      "USER@example.COM",
		RunID:      "run_12",
		PodName:    "session-12",
		Provider:   "codex",
		StreamPath: RunStreamPath("run_12"),
		PIDPath:    RunPIDPath("run_12"),
	})
	if got, want := runDoc["id"], "12"; got != want {
		t.Fatalf("active run id = %v, want %q", got, want)
	}
	if got, want := runDoc["status"], "running"; got != want {
		t.Fatalf("active run status = %v, want %q", got, want)
	}
}

func TestPodManifestCompatibilityCore(t *testing.T) {
	manifest := PodManifest("12", "nelson@romaine.life", "codex_headless", ManifestOptions{
		SessionImage:      "claude-image",
		CodexSessionImage: "codex-image",
		PiSessionImage:    "pi-image",
	})

	metadata := manifest["metadata"].(map[string]any)
	if got, want := metadata["name"], "session-12"; got != want {
		t.Fatalf("pod name = %v, want %q", got, want)
	}
	labels := metadata["labels"].(map[string]any)
	if got, want := labels["tank-operator/owner"], "u-db1458e0eb6e9e75"; got != want {
		t.Fatalf("owner label = %v, want %q", got, want)
	}
	if got, want := labels["tank-operator/mode"], CodexGUIMode; got != want {
		t.Fatalf("mode label = %v, want %q", got, want)
	}
	annotations := metadata["annotations"].(map[string]any)
	if got, want := annotations["tank-operator/owner-email"], "nelson@romaine.life"; got != want {
		t.Fatalf("owner annotation = %v, want %q", got, want)
	}

	spec := manifest["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	if got, want := len(containers), 2; got != want {
		t.Fatalf("container count = %d, want %d", got, want)
	}
	if got, want := containers[0].(map[string]any)["name"], "mcp-auth-proxy"; got != want {
		t.Fatalf("sidecar container name = %v, want %q", got, want)
	}
	claude := containers[1].(map[string]any)
	if got, want := claude["name"], "claude"; got != want {
		t.Fatalf("main container name = %v, want %q", got, want)
	}
	if got, want := claude["image"], "codex-image"; got != want {
		t.Fatalf("main container image = %v, want %q", got, want)
	}
	ports := claude["ports"].([]any)
	if got, want := ports[0].(map[string]any)["name"], "sandbox-agent"; got != want {
		t.Fatalf("main container port name = %v, want %q", got, want)
	}
	volumes := spec["volumes"].([]any)
	if got, want := len(volumes), 1; got != want {
		t.Fatalf("volume count = %d, want %d", got, want)
	}
}
