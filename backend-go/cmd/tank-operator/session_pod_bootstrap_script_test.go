package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSessionPodBootstrapScript_PerMode executes the in-pod bootstrap script
// against each wizard mode in a temp HOME and asserts the right config files
// land on disk. This is the regression guard the deletion in 650c282 (which
// labelled this script "dead") didn't have — the bootstrap is load-bearing
// for codex_config / config and a future "remove this, looks
// dead" PR should be blocked by a failing test, not by user reports.
func TestSessionPodBootstrapScript_PerMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The script uses POSIX paths and heredocs that are awkward
		// to invoke through Windows bash semantics. CI Linux runs it.
		t.Skip("bootstrap script test runs on POSIX only")
	}

	scriptPath, err := filepath.Abs("../../../k8s/session-config/session-pod-bootstrap.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("script not at expected path %s: %v", scriptPath, err)
	}

	cases := []struct {
		mode      string
		wantFiles map[string]string // path-suffix → expected-content-substring
	}{
		{
			mode: "codex_config",
			wantFiles: map[string]string{
				".codex/config.toml": `cli_auth_credentials_store = "file"`,
			},
		},
		{
			mode: "config",
			wantFiles: map[string]string{
				".claude/settings.json": `"theme":"dark"`,
				".claude.json":          `"hasCompletedOnboarding": true`,
			},
		},
		{
			mode:      "codex_cli",
			wantFiles: map[string]string{".codex/config.toml": `cli_auth_credentials_store = "file"`},
		},
		{
			mode:      "claude_gui",
			wantFiles: nil, // non-wizard, no seeding
		},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command("bash", scriptPath)
			cmd.Env = append(os.Environ(),
				"HOME="+home,
				"TANK_SESSION_MODE="+tc.mode,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
			}

			for suffix, wantSubstr := range tc.wantFiles {
				path := filepath.Join(home, suffix)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("expected file %s missing: %v", path, err)
					continue
				}
				if !strings.Contains(string(data), wantSubstr) {
					t.Errorf("file %s missing expected content %q\ngot:\n%s", path, wantSubstr, string(data))
				}
			}

			if tc.mode == "claude_gui" {
				// Defensive: no-seeding modes really must not write to HOME.
				entries, _ := os.ReadDir(home)
				if len(entries) > 0 {
					t.Errorf("non-wizard mode wrote to HOME: %v", entries)
				}
			}
		})
	}
}
