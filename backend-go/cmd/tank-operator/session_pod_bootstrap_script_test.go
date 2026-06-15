package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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

	pluginSrc, err := filepath.Abs("../../../k8s/session-config/kubectl-credential-tank.sh")
	if err != nil {
		t.Fatalf("resolve kubectl credential plugin path: %v", err)
	}

	home := t.TempDir()
	templateDir := filepath.Join(t.TempDir(), "template")
	kubeconfigPath := filepath.Join(t.TempDir(), "kube", "config")
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, []byte("fake-ca"), 0o600); err != nil {
		t.Fatalf("write fake CA: %v", err)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(isolatedGitEnv(home),
		"TANK_RESTRICTED_GIT=true",
		"AGENT_POST_COMMIT_HOOK="+hookPath,
		"AGENT_PRE_PUSH_HOOK="+prePushHookPath,
		"AGENT_GIT_TEMPLATE_DIR="+templateDir,
		// Provide the trusted-kubectl inputs too: restricted mode must still NOT
		// write a kubeconfig — the two modes stay separate.
		"AGENT_KUBECTL_CREDENTIAL_PLUGIN_SRC="+pluginSrc,
		"AGENT_KUBECONFIG_PATH="+kubeconfigPath,
		"AGENT_KUBE_CA_PATH="+caPath,
		"KUBERNETES_SERVICE_HOST=10.0.0.1",
		"KUBERNETES_SERVICE_PORT=443",
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
	// Restricted sessions must NOT get the trusted-SA kubeconfig.
	if _, err := os.Stat(kubeconfigPath); !os.IsNotExist(err) {
		t.Fatalf("kubeconfig written in restricted mode, stat err: %v", err)
	}
}

// Outside restricted git, the script must NOT install governed hooks; instead
// it wires the auto-minting credential helper so non-restricted sessions get
// full, automatic git access.
func TestInstallAgentGitTemplateScriptInstallsCredentialHelperOutsideRestrictedGit(t *testing.T) {
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
	helperSrc, err := filepath.Abs("../../../k8s/session-config/git-credential-tank.sh")
	if err != nil {
		t.Fatalf("resolve credential helper path: %v", err)
	}
	pluginSrc, err := filepath.Abs("../../../k8s/session-config/kubectl-credential-tank.sh")
	if err != nil {
		t.Fatalf("resolve kubectl credential plugin path: %v", err)
	}

	home := t.TempDir()
	templateDir := filepath.Join(t.TempDir(), "template")
	helperDst := filepath.Join(t.TempDir(), "bin", "git-credential-tank")
	pluginDst := filepath.Join(t.TempDir(), "bin", "kubectl-credential-tank")
	kubeconfigPath := filepath.Join(t.TempDir(), "kube", "config")
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, []byte("fake-ca"), 0o600); err != nil {
		t.Fatalf("write fake CA: %v", err)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(isolatedGitEnv(home),
		"AGENT_POST_COMMIT_HOOK="+hookPath,
		"AGENT_GIT_TEMPLATE_DIR="+templateDir,
		"AGENT_GIT_CREDENTIAL_HELPER_SRC="+helperSrc,
		"AGENT_GIT_CREDENTIAL_HELPER_DST="+helperDst,
		"AGENT_KUBECTL_CREDENTIAL_PLUGIN_SRC="+pluginSrc,
		"AGENT_KUBECTL_CREDENTIAL_PLUGIN_DST="+pluginDst,
		"AGENT_KUBECONFIG_PATH="+kubeconfigPath,
		"AGENT_KUBE_CA_PATH="+caPath,
		"KUBERNETES_SERVICE_HOST=10.0.0.1",
		"KUBERNETES_SERVICE_PORT=443",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed under sh: %v\noutput:\n%s", err, string(out))
	}

	helper := strings.TrimSpace(string(mustOutputEnv(t, isolatedGitEnv(home), "git", "config", "--global", "credential.helper")))
	if helper != helperDst {
		t.Fatalf("credential.helper = %q, want %q", helper, helperDst)
	}
	useHTTPPath := strings.TrimSpace(string(mustOutputEnv(t, isolatedGitEnv(home), "git", "config", "--global", "credential.useHttpPath")))
	if useHTTPPath != "true" {
		t.Fatalf("credential.useHttpPath = %q, want \"true\"", useHTTPPath)
	}
	info, err := os.Stat(helperDst)
	if err != nil {
		t.Fatalf("credential helper not installed at %s: %v", helperDst, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("credential helper %s is not executable (mode %v)", helperDst, info.Mode())
	}

	// Governed hook templates are restricted-mode only.
	if _, err := os.Stat(filepath.Join(templateDir, "hooks", "post-commit")); !os.IsNotExist(err) {
		t.Fatalf("post-commit hook installed without restricted git opt-in, stat err: %v", err)
	}

	// Non-restricted sessions also get a kubeconfig whose credential is the
	// trusted-SA exec plugin, giving kubectl cluster write.
	assertFileContains(t, kubeconfigPath, "command: "+pluginDst)
	assertFileContains(t, kubeconfigPath, "server: https://10.0.0.1:443")
	pinfo, err := os.Stat(pluginDst)
	if err != nil {
		t.Fatalf("kubectl credential plugin not installed at %s: %v", pluginDst, err)
	}
	if pinfo.Mode()&0o111 == 0 {
		t.Fatalf("kubectl credential plugin %s is not executable (mode %v)", pluginDst, pinfo.Mode())
	}
}

// The credential helper mints a repo-scoped token through the in-pod MCP and
// hands git the x-access-token credential. It must request the full/write
// scope, scope to the requested repo, parse the SSE-framed MCP reply, and
// bail on non-github hosts.
func TestGitCredentialTankHelperMintsToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential helper script test runs on POSIX only")
	}

	helperSrc, err := filepath.Abs("../../../k8s/session-config/git-credential-tank.sh")
	if err != nil {
		t.Fatalf("resolve credential helper path: %v", err)
	}

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		// MCP streamable-HTTP frames the JSON-RPC result as an SSE event.
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_testtoken\",\"expires_at\":\"2099-01-01T00:00:00Z\"}}}\n\n"))
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	run := func(reqPath, host string) string {
		cmd := exec.Command("sh", helperSrc, "get")
		cmd.Env = []string{
			"TANK_GIT_CRED_MCP_URL=" + srv.URL,
			"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
			"PATH=" + os.Getenv("PATH"),
		}
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\npath=" + reqPath + "\n\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper failed: %v\noutput:\n%s", err, string(out))
		}
		return string(out)
	}

	out := run("romaine-life/tank-operator.git", "github.com")
	if !strings.Contains(out, "username=x-access-token") || !strings.Contains(out, "password=ghs_testtoken") {
		t.Fatalf("helper output missing credential, got:\n%s", out)
	}
	if !strings.Contains(gotBody, "\"mint_clone_token\"") ||
		!strings.Contains(gotBody, "romaine-life/tank-operator") ||
		!strings.Contains(gotBody, "\"full\":true") {
		t.Fatalf("mint request body unexpected: %s", gotBody)
	}

	// Non-github host: the helper must produce no credential.
	if out := run("romaine-life/tank-operator.git", "example.com"); strings.TrimSpace(out) != "" {
		t.Fatalf("helper emitted credential for non-github host: %q", out)
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
