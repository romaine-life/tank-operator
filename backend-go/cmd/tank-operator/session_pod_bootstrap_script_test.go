package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallTankDocsScriptRunsUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("docs install script test runs on POSIX only")
	}

	scriptPath, err := filepath.Abs("../../../k8s/session-config/install-tank-docs.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	configDir := t.TempDir()
	destRoot := filepath.Join(t.TempDir(), "docs")
	if err := os.WriteFile(filepath.Join(configDir, "docs__quality-timeframes.md"), []byte("quality"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "docs__nested__migration-policy.md"), []byte("migration"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(isolatedScriptEnv(t.TempDir()),
		"INSTALL_TANK_DOCS_CONFIG_DIR="+configDir,
		"INSTALL_TANK_DOCS_DEST_ROOT="+destRoot,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed under sh: %v\noutput:\n%s", err, string(out))
	}

	assertFileContains(t, filepath.Join(destRoot, "quality-timeframes.md"), "quality")
	assertFileContains(t, filepath.Join(destRoot, "nested", "migration-policy.md"), "migration")
}

func TestInstallTankSkillsScriptRunsUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skills install script test runs on POSIX only")
	}

	scriptPath, err := filepath.Abs("../../../k8s/session-config/install-tank-skills.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	configDir := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "skills__common__north-star__SKILL.md"), []byte("north"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "skills__common__rollout__agents__openai.yaml"), []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(isolatedScriptEnv(home),
		"INSTALL_TANK_SKILLS_CONFIG_DIR="+configDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed under sh: %v\noutput:\n%s", err, string(out))
	}

	assertFileContains(t, filepath.Join(home, ".claude", "skills", "north-star", "SKILL.md"), "north")
	assertFileContains(t, filepath.Join(home, ".codex", "skills", "north-star", "SKILL.md"), "north")
}

func TestInstallAgentPostCommitReminderScriptRunsUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook install script test runs on POSIX only")
	}

	sourceScript, err := filepath.Abs("../../../scripts/install-agent-post-commit-reminder.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	sourceHook, err := filepath.Abs("../../../.githooks/post-commit")
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
	}
	sourcePrePushHook, err := filepath.Abs("../../../.githooks/pre-push")
	if err != nil {
		t.Fatalf("resolve pre-push hook path: %v", err)
	}

	repoDir := t.TempDir()
	home := t.TempDir()
	env := isolatedGitEnv(home)
	mustRunEnv(t, repoDir, env, "git", "init")
	mustRun(t, repoDir, "mkdir", "-p", "scripts", ".githooks")
	copyFile(t, sourceScript, filepath.Join(repoDir, "scripts", "install-agent-post-commit-reminder.sh"), 0o755)
	copyFile(t, sourceHook, filepath.Join(repoDir, ".githooks", "post-commit"), 0o755)
	copyFile(t, sourcePrePushHook, filepath.Join(repoDir, ".githooks", "pre-push"), 0o755)

	mustRunEnv(t, repoDir, env, "sh", "scripts/install-agent-post-commit-reminder.sh")
	assertFileContains(t, filepath.Join(repoDir, ".git", "hooks", "post-commit"), "[tank-agent] Local commit created")
	assertFileContains(t, filepath.Join(repoDir, ".git", "hooks", "pre-push"), "[tank-agent] Direct git push is disabled")
}

func TestInstallAgentPostCommitReminderReplacesManagedTemplateHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook install script test runs on POSIX only")
	}

	sourceScript, err := filepath.Abs("../../../scripts/install-agent-post-commit-reminder.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	sourceHook, err := filepath.Abs("../../../.githooks/post-commit")
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
	}
	sourcePrePushHook, err := filepath.Abs("../../../.githooks/pre-push")
	if err != nil {
		t.Fatalf("resolve pre-push hook path: %v", err)
	}

	repoDir := t.TempDir()
	home := t.TempDir()
	env := isolatedGitEnv(home)
	mustRunEnv(t, repoDir, env, "git", "init")
	mustRun(t, repoDir, "mkdir", "-p", "scripts", ".githooks")
	copyFile(t, sourceScript, filepath.Join(repoDir, "scripts", "install-agent-post-commit-reminder.sh"), 0o755)
	copyFile(t, sourceHook, filepath.Join(repoDir, ".githooks", "post-commit"), 0o755)
	copyFile(t, sourcePrePushHook, filepath.Join(repoDir, ".githooks", "pre-push"), 0o755)
	writeExecutable(t, filepath.Join(repoDir, ".git", "hooks", "post-commit"), `#!/bin/sh
echo '[tank-agent] Local commit created.'
`)

	mustRunEnv(t, repoDir, env, "sh", "scripts/install-agent-post-commit-reminder.sh")
	assertFileContains(t, filepath.Join(repoDir, ".git", "hooks", "post-commit"), "exec sh /opt/tank/session-config/agent-post-commit-hook.sh")
}

func TestInstallAgentPostCommitReminderRefusesUnmanagedHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook install script test runs on POSIX only")
	}

	sourceScript, err := filepath.Abs("../../../scripts/install-agent-post-commit-reminder.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	sourceHook, err := filepath.Abs("../../../.githooks/post-commit")
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
	}
	sourcePrePushHook, err := filepath.Abs("../../../.githooks/pre-push")
	if err != nil {
		t.Fatalf("resolve pre-push hook path: %v", err)
	}

	repoDir := t.TempDir()
	home := t.TempDir()
	env := isolatedGitEnv(home)
	mustRunEnv(t, repoDir, env, "git", "init")
	mustRun(t, repoDir, "mkdir", "-p", "scripts", ".githooks")
	copyFile(t, sourceScript, filepath.Join(repoDir, "scripts", "install-agent-post-commit-reminder.sh"), 0o755)
	copyFile(t, sourceHook, filepath.Join(repoDir, ".githooks", "post-commit"), 0o755)
	copyFile(t, sourcePrePushHook, filepath.Join(repoDir, ".githooks", "pre-push"), 0o755)
	writeExecutable(t, filepath.Join(repoDir, ".git", "hooks", "post-commit"), `#!/bin/sh
echo custom
`)

	cmd := exec.Command("sh", "scripts/install-agent-post-commit-reminder.sh")
	cmd.Dir = repoDir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("installer succeeded, want refusal for unmanaged hook\noutput:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Refusing to replace existing local hook") {
		t.Fatalf("installer output missing refusal:\n%s", string(out))
	}
	assertFileContains(t, filepath.Join(repoDir, ".git", "hooks", "post-commit"), "echo custom")
}

func TestInstallAgentGitTemplateScriptRunsUnderSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("git template install script test runs on POSIX only")
	}

	scriptPath, err := filepath.Abs("../../../k8s/session-config/install-agent-git-template.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	hookPath, err := filepath.Abs("../../../k8s/session-config/agent-post-commit-hook.sh")
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
	}
	prePushHookPath, err := filepath.Abs("../../../k8s/session-config/agent-pre-push-hook.sh")
	if err != nil {
		t.Fatalf("resolve pre-push hook path: %v", err)
	}

	home := t.TempDir()
	templateDir := filepath.Join(t.TempDir(), "template")
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(isolatedGitEnv(home),
		"TANK_RESTRICTED_GIT=true",
		"AGENT_POST_COMMIT_HOOK="+hookPath,
		"AGENT_PRE_PUSH_HOOK="+prePushHookPath,
		"AGENT_GIT_TEMPLATE_DIR="+templateDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed under sh: %v\noutput:\n%s", err, string(out))
	}

	assertFileContains(t, filepath.Join(templateDir, "hooks", "post-commit"), "[tank-agent] Local commit created")
	assertFileContains(t, filepath.Join(templateDir, "hooks", "pre-push"), "[tank-agent] Direct git push is disabled")
	configured := strings.TrimSpace(string(mustOutputEnv(t, isolatedGitEnv(home), "git", "config", "--global", "init.templateDir")))
	if configured != templateDir {
		t.Fatalf("init.templateDir = %q, want %q", configured, templateDir)
	}
}

func TestInstallAgentGitTemplateScriptNoopsOutsideRestrictedGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("git template install script test runs on POSIX only")
	}

	scriptPath, err := filepath.Abs("../../../k8s/session-config/install-agent-git-template.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}
	hookPath, err := filepath.Abs("../../../k8s/session-config/agent-post-commit-hook.sh")
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
	}

	home := t.TempDir()
	templateDir := filepath.Join(t.TempDir(), "template")
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(isolatedGitEnv(home),
		"AGENT_POST_COMMIT_HOOK="+hookPath,
		"AGENT_GIT_TEMPLATE_DIR="+templateDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed under sh: %v\noutput:\n%s", err, string(out))
	}
	if _, err := os.Stat(filepath.Join(templateDir, "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatalf("post-commit hook installed without restricted git opt-in, stat err: %v", err)
	}
}

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
	gitTemplateScript, err := filepath.Abs("../../../k8s/session-config/install-agent-git-template.sh")
	if err != nil {
		t.Fatalf("resolve git template script path: %v", err)
	}
	hookPath, err := filepath.Abs("../../../k8s/session-config/agent-post-commit-hook.sh")
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
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
				".claude/settings.json": `"theme": "dark"`,
				".claude.json":          `"hasCompletedOnboarding": true`,
			},
		},
		{
			mode: "claude_secondary_config",
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
			mode: "claude_gui",
			wantFiles: map[string]string{
				".claude/settings.json": `"skipDangerousModePermissionPrompt":true`,
			},
		},
		{
			mode: "claude_secondary_gui",
			wantFiles: map[string]string{
				".claude/settings.json": `"skipDangerousModePermissionPrompt":true`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			home := t.TempDir()
			templateDir := filepath.Join(home, ".config", "tank", "git-template")
			cmd := exec.Command("bash", scriptPath)
			cmd.Env = append(isolatedGitEnv(home),
				"TANK_SESSION_MODE="+tc.mode,
				"TANK_RESTRICTED_GIT=true",
				"INSTALL_AGENT_GIT_TEMPLATE_SCRIPT="+gitTemplateScript,
				"AGENT_POST_COMMIT_HOOK="+hookPath,
				"AGENT_GIT_TEMPLATE_DIR="+templateDir,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
			}

			assertFileContains(t, filepath.Join(templateDir, "hooks", "post-commit"), "[tank-agent] Local commit created")
			configured := strings.TrimSpace(string(mustOutputEnv(t, isolatedGitEnv(home), "git", "config", "--global", "init.templateDir")))
			if configured != templateDir {
				t.Fatalf("init.templateDir = %q, want %q", configured, templateDir)
			}

			for suffix, wantSubstr := range tc.wantFiles {
				path := filepath.Join(home, suffix)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Errorf("expected file %s missing: %v", path, err)
					continue
				}
				got := string(data)
				if !containsIgnoringWhitespace(got, wantSubstr) {
					t.Errorf("file %s missing expected content %q\ngot:\n%s", path, wantSubstr, string(data))
				}
			}

			if tc.mode == "claude_gui" || tc.mode == "claude_secondary_gui" {
				// Non-wizard modes still get the shared git template
				// bootstrap and Claude runtime settings. They must not
				// receive provider wizard onboarding or unrelated seeds.
				for _, suffix := range []string{
					".codex/config.toml",
					".claude.json",
				} {
					path := filepath.Join(home, suffix)
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						data, _ := os.ReadFile(path)
						t.Errorf("non-wizard mode wrote provider seed %s: %v\ncontent:\n%s\nscript output:\n%s", path, err, string(data), string(out))
					}
				}
			}
		})
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file %s missing: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("file %s missing expected content %q\ngot:\n%s", path, want, string(data))
	}
}

func containsIgnoringWhitespace(got, want string) bool {
	if strings.Contains(got, want) {
		return true
	}
	return strings.Contains(stripWhitespace(got), stripWhitespace(want))
}

func stripWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, value)
}

func TestSessionPodBootstrapScript_SpireLensTailnetOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bootstrap script test runs on POSIX only")
	}

	scriptPath, err := filepath.Abs("../../../k8s/session-config/session-pod-bootstrap.sh")
	if err != nil {
		t.Fatalf("resolve script path: %v", err)
	}

	home := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	tailnetLog := filepath.Join(t.TempDir(), "tailnet.log")
	curlLog := filepath.Join(t.TempDir(), "curl.log")
	socketPath := filepath.Join(t.TempDir(), "tailscaled.sock")
	stateDir := filepath.Join(t.TempDir(), "state")
	tokenPath := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenPath, []byte("pod-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	writeExecutable(t, filepath.Join(fakeBin, "jq"), `#!/bin/sh
if [ "$1" = "-nc" ]; then
  printf '{}\n'
  exit 0
fi
if [ "$1" = "-r" ]; then
  filter="$2"
  case "$filter" in
    *access_token*) printf 'ts-access\n' ;;
    *token*) printf 'fed-jwt\n' ;;
    *key*) printf 'tskey-auth\n' ;;
    *) exit 1 ;;
  esac
  exit 0
fi
exit 1
`)
	writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
printf 'curl %s\n' "$*" >> "$FAKE_CURL_LOG"
case "$*" in
  *exchange/federation*) printf '{"token":"fed-jwt"}\n' ;;
  *oauth/token-exchange*) printf '{"access_token":"ts-access"}\n' ;;
  *'/keys'*) printf '{"key":"tskey-auth"}\n' ;;
  *) exit 1 ;;
esac
`)
	writeExecutable(t, filepath.Join(fakeBin, "tailscaled"), `#!/bin/sh
printf 'tailscaled %s\n' "$*" >> "$FAKE_TAILNET_LOG"
socket=""
for arg in "$@"; do
  case "$arg" in
    --socket=*) socket="${arg#--socket=}" ;;
  esac
done
if [ -n "$socket" ]; then
  socket_dir="${socket%/*}"
  if [ "$socket_dir" != "$socket" ]; then
    mkdir -p "$socket_dir"
  fi
  : > "$socket"
fi
exit 0
`)
	writeExecutable(t, filepath.Join(fakeBin, "tailscale"), `#!/bin/sh
printf 'tailscale %s\n' "$*" >> "$FAKE_TAILNET_LOG"
case "$*" in
  *status*) exit 1 ;;
esac
exit 0
`)

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(isolatedScriptEnvWithPath(home, fakeBin+string(os.PathListSeparator)+os.Getenv("PATH")),
		"TANK_SESSION_MODE=claude_gui",
		"SPIRELENS_MCP_ENABLED=true",
		"AUTH_ROMAINE_TOKEN_PATH="+tokenPath,
		"AUTH_ROMAINE_URL=https://auth.romaine.life",
		"SPIRELENS_TAILSCALE_OIDC_CLIENT_ID=oidc-client",
		"SPIRELENS_TAILSCALE_TAILNET=-",
		"SPIRELENS_TAILSCALE_AUTH_TAG=tag:spirelens-orchestrator",
		"SPIRELENS_TAILSCALE_SOCKET="+socketPath,
		"SPIRELENS_TAILSCALE_STATE_DIR="+stateDir,
		"SPIRELENS_TAILSCALE_OUTBOUND_HTTP_PROXY_LISTEN=127.0.0.1:1055",
		"SPIRELENS_TAILSCALE_HOSTNAME=session-test",
		"SPIRELENS_TAILSCALE_AUTHKEY_EXPIRY_SECONDS=1200",
		"FAKE_TAILNET_LOG="+tailnetLog,
		"FAKE_CURL_LOG="+curlLog,
		"WRITE_CLAUDE_SETTINGS_SCRIPT="+filepath.Join(t.TempDir(), "missing-write-claude-settings.sh"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\noutput:\n%s", err, string(out))
	}

	tailnetRaw, err := os.ReadFile(tailnetLog)
	if err != nil {
		t.Fatalf("read tailnet log: %v", err)
	}
	tailnet := string(tailnetRaw)
	for _, want := range []string{
		"tailscaled --tun=userspace-networking",
		"--statedir=" + stateDir,
		"--socket=" + socketPath,
		"--outbound-http-proxy-listen=127.0.0.1:1055",
		"tailscale --socket=" + socketPath + " up --authkey=tskey-auth --hostname=session-test --accept-routes=false --accept-dns=false",
	} {
		if !strings.Contains(tailnet, want) {
			t.Fatalf("tailnet log missing %q\ngot:\n%s", want, tailnet)
		}
	}

	curlRaw, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatalf("read curl log: %v", err)
	}
	curlCalls := string(curlRaw)
	for _, want := range []string{
		"/api/auth/exchange/federation",
		"oauth/token-exchange",
		"/api/v2/tailnet/-/keys",
	} {
		if !strings.Contains(curlCalls, want) {
			t.Fatalf("curl log missing %q\ngot:\n%s", want, curlCalls)
		}
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	content, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, content, mode); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\noutput:\n%s", name, strings.Join(args, " "), err, string(out))
	}
}

func mustRunEnv(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\noutput:\n%s", name, strings.Join(args, " "), err, string(out))
	}
}

func mustOutputEnv(t *testing.T, env []string, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\noutput:\n%s", name, strings.Join(args, " "), err, string(out))
	}
	return out
}

func isolatedGitEnv(home string) []string {
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
	}
	if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	return env
}

func isolatedScriptEnv(home string) []string {
	return isolatedScriptEnvWithPath(home, os.Getenv("PATH"))
}

func isolatedScriptEnvWithPath(home, path string) []string {
	env := []string{
		"HOME=" + home,
		"TMPDIR=" + os.TempDir(),
	}
	if path != "" {
		env = append(env, "PATH="+path)
	}
	if user := os.Getenv("USER"); user != "" {
		env = append(env, "USER="+user)
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		env = append(env, "SHELL="+shell)
	}
	return env
}
