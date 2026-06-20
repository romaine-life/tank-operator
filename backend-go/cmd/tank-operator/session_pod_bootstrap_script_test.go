package main

import (
	"encoding/json"
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
		// Restricted mode now ALSO installs the (mode-aware, read-only) credential
		// helper, so reads work via git; provide its inputs and assert it lands.
		"AGENT_GIT_CREDENTIAL_HELPER_SRC="+helperSrc,
		"AGENT_GIT_CREDENTIAL_HELPER_DST="+helperDst,
		// Provide the trusted-kubectl inputs too: restricted mode must still NOT
		// write a kubeconfig — the elevated kubectl path stays non-restricted-only.
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
	// Restricted sessions ALSO get the credential helper (read-only minting), so
	// git reads work. The pre-push hook still blocks pushes; the helper's token
	// cannot push anyway.
	helper := strings.TrimSpace(string(mustOutputEnv(t, isolatedGitEnv(home), "git", "config", "--global", "credential.helper")))
	if helper != helperDst {
		t.Fatalf("credential.helper = %q, want %q", helper, helperDst)
	}
	useHTTPPath := strings.TrimSpace(string(mustOutputEnv(t, isolatedGitEnv(home), "git", "config", "--global", "credential.useHttpPath")))
	if useHTTPPath != "true" {
		t.Fatalf("credential.useHttpPath = %q, want \"true\"", useHTTPPath)
	}
	if info, err := os.Stat(helperDst); err != nil {
		t.Fatalf("credential helper not installed at %s: %v", helperDst, err)
	} else if info.Mode()&0o111 == 0 {
		t.Fatalf("credential helper %s is not executable (mode %v)", helperDst, info.Mode())
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
// hands git the x-access-token credential. It must scope to the requested repo,
// parse the SSE-framed MCP reply, bail on non-github hosts, and request a
// mode-aware scope: the App's full/write set in non-restricted mode, and a
// read-only token (write:false, no full) in restricted mode.
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

	run := func(reqPath, host string, restricted bool) string {
		cmd := exec.Command("sh", helperSrc, "get")
		cmd.Env = []string{
			"TANK_GIT_CRED_MCP_URL=" + srv.URL,
			"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
			"PATH=" + os.Getenv("PATH"),
		}
		if restricted {
			cmd.Env = append(cmd.Env, "TANK_RESTRICTED_GIT=true")
		}
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\npath=" + reqPath + "\n\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper failed: %v\noutput:\n%s", err, string(out))
		}
		return string(out)
	}

	// Non-restricted: the helper mints the App's full/write scope.
	out := run("romaine-life/tank-operator.git", "github.com", false)
	if !strings.Contains(out, "username=x-access-token") || !strings.Contains(out, "password=ghs_testtoken") {
		t.Fatalf("helper output missing credential, got:\n%s", out)
	}
	if !strings.Contains(gotBody, "\"mint_clone_token\"") ||
		!strings.Contains(gotBody, "romaine-life/tank-operator") ||
		!strings.Contains(gotBody, "\"full\":true") {
		t.Fatalf("non-restricted mint request body unexpected: %s", gotBody)
	}

	// Restricted: the helper mints a read-only token (write:false, no full) so
	// reads work without a push-capable credential in the shell.
	out = run("romaine-life/tank-operator.git", "github.com", true)
	if !strings.Contains(out, "username=x-access-token") || !strings.Contains(out, "password=ghs_testtoken") {
		t.Fatalf("restricted helper output missing credential, got:\n%s", out)
	}
	if !strings.Contains(gotBody, "\"mint_clone_token\"") ||
		!strings.Contains(gotBody, "romaine-life/tank-operator") ||
		!strings.Contains(gotBody, "\"write\":false") ||
		strings.Contains(gotBody, "\"full\":true") {
		t.Fatalf("restricted mint request body should be read-only, got: %s", gotBody)
	}

	// Non-github host: the helper must produce no credential (either mode).
	if out := run("romaine-life/tank-operator.git", "example.com", false); strings.TrimSpace(out) != "" {
		t.Fatalf("helper emitted credential for non-github host: %q", out)
	}
}

// The gh wrapper (/usr/local/bin/gh) mints a fresh token via the in-pod MCP and
// execs the real gh with it. Scope is mode-aware: full/write when non-restricted,
// read-only (write:false, no full) when restricted — so `gh` reads work in
// restricted mode without handing the shell a push-capable credential.
func TestGhTankWrapperMintsModeAwareToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh wrapper script test runs on POSIX only")
	}

	wrapperSrc, err := filepath.Abs("../../../session-images/gh-tank-wrapper.sh")
	if err != nil {
		t.Fatalf("resolve gh wrapper path: %v", err)
	}

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_testtoken\",\"expires_at\":\"2099-01-01T00:00:00Z\"}}}\n\n"))
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	// Fake real gh: echo the GH_TOKEN the wrapper exported so we can assert it.
	fakeGh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fakeGh, []byte("#!/bin/sh\nprintf 'GH_TOKEN=%s\\n' \"$GH_TOKEN\"\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	// Empty workspace so repo scope comes only from the explicit --repo arg,
	// making the test independent of the ambient /workspace.
	emptyWorkspace := t.TempDir()

	run := func(restricted bool) (string, string) {
		gotBody = ""
		env := []string{
			"TANK_REAL_GH=" + fakeGh,
			"TANK_GIT_CRED_MCP_URL=" + srv.URL,
			"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
			"TANK_WORKSPACE_DIR=" + emptyWorkspace,
			"PATH=" + os.Getenv("PATH"),
		}
		if restricted {
			env = append(env, "TANK_RESTRICTED_GIT=true")
		}
		cmd := exec.Command("sh", wrapperSrc, "pr", "view", "--repo", "romaine-life/tank-operator")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wrapper failed (restricted=%v): %v\noutput:\n%s", restricted, err, string(out))
		}
		return string(out), gotBody
	}

	// Non-restricted: full/write scope, token handed to gh.
	out, body := run(false)
	if !strings.Contains(out, "GH_TOKEN=ghs_testtoken") {
		t.Fatalf("non-restricted wrapper did not export minted token to gh: %s", out)
	}
	if !strings.Contains(body, "\"mint_clone_token\"") ||
		!strings.Contains(body, "romaine-life/tank-operator") ||
		!strings.Contains(body, "\"full\":true") {
		t.Fatalf("non-restricted gh mint request unexpected: %s", body)
	}

	// Restricted: read-only scope, but gh is still authenticated for reads.
	out, body = run(true)
	if !strings.Contains(out, "GH_TOKEN=ghs_testtoken") {
		t.Fatalf("restricted wrapper did not export minted token to gh: %s", out)
	}
	if !strings.Contains(body, "\"mint_clone_token\"") ||
		!strings.Contains(body, "romaine-life/tank-operator") ||
		!strings.Contains(body, "\"write\":false") ||
		strings.Contains(body, "\"full\":true") {
		t.Fatalf("restricted gh mint should be read-only, got: %s", body)
	}
}

// `gh pr create` on a Tank session branch is delegated to the governed sidecar
// endpoint (the agent shell never receives a write token); every other gh verb,
// and pr create off a non-session branch, falls through to the real gh.
func TestGhTankWrapperDelegatesPrCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh wrapper script test runs on POSIX only")
	}
	wrapperSrc, err := filepath.Abs("../../../session-images/gh-tank-wrapper.sh")
	if err != nil {
		t.Fatalf("resolve gh wrapper path: %v", err)
	}

	var prBody string
	var prHits int
	prSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prHits++
		b, _ := io.ReadAll(r.Body)
		prBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"created":true,"pr_url":"https://github.com/romaine-life/tank-operator/pull/77","pr_number":77}`))
	}))
	defer prSrv.Close()

	// Fake mint server for the fall-through (non-delegated) path.
	mintSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_ro\"}}}\n\n"))
	}))
	defer mintSrv.Close()

	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	// Fake real gh marks the fall-through path so we can assert non-delegation.
	fakeGh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fakeGh, []byte("#!/bin/sh\nprintf 'REALGH %s\\n' \"$*\"\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}

	// A real git repo on a Tank session branch with a GitHub origin.
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "a@b.test")
	git("config", "user.name", "A")
	git("checkout", "-q", "-b", "tank/session/95/tank-operator")
	git("commit", "-q", "--allow-empty", "-m", "start")
	git("remote", "add", "origin", "https://github.com/romaine-life/tank-operator.git")

	runWrapper := func(branch string, args ...string) string {
		co := exec.Command("git", "checkout", "-q", "-B", branch)
		co.Dir = repo
		if out, err := co.CombinedOutput(); err != nil {
			t.Fatalf("checkout %s: %v\n%s", branch, err, out)
		}
		cmd := exec.Command("sh", append([]string{wrapperSrc}, args...)...)
		cmd.Dir = repo
		cmd.Env = []string{
			"TANK_RESTRICTED_GIT=true",
			"TANK_REAL_GH=" + fakeGh,
			"TANK_GIT_CRED_MCP_URL=" + mintSrv.URL + "/",
			"TANK_CREATE_SESSION_PR_URL=" + prSrv.URL + "/create-session-pr",
			"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
			"TANK_WORKSPACE_DIR=" + t.TempDir(),
			"PATH=" + os.Getenv("PATH"),
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wrapper %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// pr create on the session branch -> delegated; prints the PR URL; no real gh.
	prHits = 0
	out := runWrapper("tank/session/95/tank-operator", "pr", "create", "--title", "My PR", "--body", "Why")
	if !strings.Contains(out, "https://github.com/romaine-life/tank-operator/pull/77") {
		t.Fatalf("delegated pr create did not print the PR URL: %q", out)
	}
	if strings.Contains(out, "REALGH") {
		t.Fatalf("pr create should not fall through to real gh: %q", out)
	}
	if prHits != 1 {
		t.Fatalf("expected exactly one delegation request, got %d", prHits)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(prBody), &sent); err != nil {
		t.Fatalf("delegation body not JSON: %v (%q)", err, prBody)
	}
	if sent["title"] != "My PR" || sent["body"] != "Why" {
		t.Fatalf("delegation did not forward title/body: %v", sent)
	}
	if rp, _ := sent["repo_path"].(string); rp == "" {
		t.Fatalf("delegation did not send repo_path: %v", sent)
	}

	// pr view is not a create -> falls through to real gh, no delegation.
	prHits = 0
	out = runWrapper("tank/session/95/tank-operator", "pr", "view")
	if prHits != 0 {
		t.Fatalf("pr view must not hit the create endpoint, got %d", prHits)
	}
	if !strings.Contains(out, "REALGH") {
		t.Fatalf("pr view should fall through to real gh: %q", out)
	}

	// pr create on a non-session branch -> falls through, no delegation.
	prHits = 0
	out = runWrapper("feature/x", "pr", "create", "--title", "X")
	if prHits != 0 {
		t.Fatalf("pr create on a non-session branch must not delegate, got %d", prHits)
	}
	if !strings.Contains(out, "REALGH") {
		t.Fatalf("non-session pr create should fall through to real gh: %q", out)
	}
}

// In restricted mode, the git credential helper first consults the in-pod
// break-glass server (the grant source of truth). An active unlimited grant
// yields a FULL token automatically (no read-only fallback); no grant falls
// back to the read-only mint exactly as before.
func TestGitCredentialTankHelperBreakGlassElevation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential helper script test runs on POSIX only")
	}
	helperSrc, err := filepath.Abs("../../../k8s/session-config/git-credential-tank.sh")
	if err != nil {
		t.Fatalf("resolve credential helper path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	run := func(breakGlassURL, readOnlyURL string) string {
		cmd := exec.Command("sh", helperSrc, "get")
		cmd.Env = []string{
			"TANK_RESTRICTED_GIT=true",
			"TANK_BREAK_GLASS_MINT_URL=" + breakGlassURL,
			"TANK_GIT_CRED_MCP_URL=" + readOnlyURL,
			"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
			"PATH=" + os.Getenv("PATH"),
		}
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=romaine-life/tank-operator.git\n\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper failed: %v\noutput:\n%s", err, string(out))
		}
		return string(out)
	}

	t.Run("active grant mints full token without read-only fallback", func(t *testing.T) {
		var bgBody string
		bg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			bgBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":true,"token":"ghs_fulltoken","full_github_api":true}`))
		}))
		defer bg.Close()
		readOnlyHit := false
		ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			readOnlyHit = true
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ro.Close()

		out := run(bg.URL, ro.URL)
		if !strings.Contains(out, "username=x-access-token") || !strings.Contains(out, "password=ghs_fulltoken") {
			t.Fatalf("expected full break-glass token, got:\n%s", out)
		}
		if readOnlyHit {
			t.Fatalf("read-only mint must not be hit when a grant is active")
		}
		if !strings.Contains(bgBody, "romaine-life/tank-operator") {
			t.Fatalf("break-glass request missing repo: %s", bgBody)
		}
	})

	t.Run("no grant falls back to read-only mint", func(t *testing.T) {
		bg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":false}`))
		}))
		defer bg.Close()
		var roBody string
		ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			roBody = string(body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_readonly\"}}}\n\n"))
		}))
		defer ro.Close()

		out := run(bg.URL, ro.URL)
		if !strings.Contains(out, "password=ghs_readonly") {
			t.Fatalf("expected read-only fallback token, got:\n%s", out)
		}
		if !strings.Contains(roBody, "\"write\":false") || strings.Contains(roBody, "\"full\":true") {
			t.Fatalf("fallback mint should be read-only, got: %s", roBody)
		}
	})

	// FAIL LOUD, never silent. When the break-glass mint returns an
	// unrecognized/error shape — e.g. the JSON-RPC `invalid MCP request` (HTTP
	// 200) the :9999 MCP catch-all emits when the mcp-auth-proxy sidecar predates
	// the /mint-git-token route (image/version skew) — the helper must NOT
	// silently collapse to read-only. It surfaces a diagnostic to stderr AND
	// still mints the read-only token so reads keep working. The silent collapse
	// is the bug that made the live regression undiagnosable.
	t.Run("error mint response fails loud and falls back to read-only", func(t *testing.T) {
		bg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid MCP request"}}`))
		}))
		defer bg.Close()
		roHit := false
		ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			roHit = true
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_readonly\"}}}\n\n"))
		}))
		defer ro.Close()

		out := run(bg.URL, ro.URL)
		if !roHit {
			t.Fatalf("read-only mint must be hit as fallback when the break-glass mint errors")
		}
		if !strings.Contains(out, "password=ghs_readonly") {
			t.Fatalf("expected read-only fallback token after error mint response, got:\n%s", out)
		}
		if !strings.Contains(out, "break-glass elevation FAILED") {
			t.Fatalf("expected a loud diagnostic on an error mint response, got:\n%s", out)
		}
	})
}

// The gh wrapper mirrors the credential helper: an active unlimited grant makes
// the in-pod break-glass server hand `gh` a FULL token; no grant falls back to
// the read-only mint.
func TestGhTankWrapperBreakGlassElevation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh wrapper script test runs on POSIX only")
	}
	wrapperSrc, err := filepath.Abs("../../../session-images/gh-tank-wrapper.sh")
	if err != nil {
		t.Fatalf("resolve gh wrapper path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	fakeGh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fakeGh, []byte("#!/bin/sh\nprintf 'GH_TOKEN=%s\\n' \"$GH_TOKEN\"\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	emptyWorkspace := t.TempDir()

	run := func(breakGlassURL, readOnlyURL string) string {
		cmd := exec.Command("sh", wrapperSrc, "pr", "view", "--repo", "romaine-life/tank-operator")
		cmd.Env = []string{
			"TANK_RESTRICTED_GIT=true",
			"TANK_REAL_GH=" + fakeGh,
			"TANK_BREAK_GLASS_MINT_URL=" + breakGlassURL,
			"TANK_GIT_CRED_MCP_URL=" + readOnlyURL,
			"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
			"TANK_WORKSPACE_DIR=" + emptyWorkspace,
			"PATH=" + os.Getenv("PATH"),
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wrapper failed: %v\noutput:\n%s", err, string(out))
		}
		return string(out)
	}

	t.Run("active grant exports full token without read-only fallback", func(t *testing.T) {
		var bgBody string
		bg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			bgBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":true,"token":"ghs_fulltoken"}`))
		}))
		defer bg.Close()
		readOnlyHit := false
		ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			readOnlyHit = true
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ro.Close()

		out := run(bg.URL, ro.URL)
		if !strings.Contains(out, "GH_TOKEN=ghs_fulltoken") {
			t.Fatalf("expected gh to receive full break-glass token, got:\n%s", out)
		}
		if readOnlyHit {
			t.Fatalf("read-only mint must not be hit when a grant is active")
		}
		if !strings.Contains(bgBody, "romaine-life/tank-operator") {
			t.Fatalf("break-glass request missing repo: %s", bgBody)
		}
	})

	t.Run("no grant falls back to read-only mint", func(t *testing.T) {
		bg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":false}`))
		}))
		defer bg.Close()
		var roBody string
		ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			roBody = string(body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_readonly\"}}}\n\n"))
		}))
		defer ro.Close()

		out := run(bg.URL, ro.URL)
		if !strings.Contains(out, "GH_TOKEN=ghs_readonly") {
			t.Fatalf("expected read-only fallback token, got:\n%s", out)
		}
		if !strings.Contains(roBody, "\"write\":false") || strings.Contains(roBody, "\"full\":true") {
			t.Fatalf("fallback mint should be read-only, got: %s", roBody)
		}
	})

	// FAIL LOUD, never silent — mirror of the credential-helper guard. A
	// JSON-RPC error (HTTP 200) from the :9999 MCP catch-all (sidecar predating
	// the /mint-git-token route) must produce a stderr diagnostic, not a silent
	// downgrade, while gh still gets a read-only token for reads.
	t.Run("error mint response fails loud and falls back to read-only", func(t *testing.T) {
		bg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid MCP request"}}`))
		}))
		defer bg.Close()
		roHit := false
		ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			roHit = true
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_readonly\"}}}\n\n"))
		}))
		defer ro.Close()

		out := run(bg.URL, ro.URL)
		if !roHit {
			t.Fatalf("read-only mint must be hit as fallback when the break-glass mint errors")
		}
		if !strings.Contains(out, "GH_TOKEN=ghs_readonly") {
			t.Fatalf("expected read-only fallback token after error mint response, got:\n%s", out)
		}
		if !strings.Contains(out, "break-glass elevation FAILED") {
			t.Fatalf("expected a loud diagnostic on an error mint response, got:\n%s", out)
		}
	})
}

// The pre-push hook brokers a branch-scoped push through Tank's in-pod
// break-glass server (:9999). When /push-head returns ok:true the commits are
// already on the remote (Tank pushed server-side); the hook reports the
// governed push SUCCEEDED and then exits non-zero so git's own credential-less
// push does not run and confuse the user. A branch_out_of_scope refusal also
// blocks with an explanatory message; anything else (incl. no_grant) falls
// through to the preserved "Direct git push is disabled" block + exit 1.
func TestAgentPrePushHookGovernedPush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pre-push hook script test runs on POSIX only")
	}
	hookSrc, err := filepath.Abs("../../../k8s/session-config/agent-pre-push-hook.sh")
	if err != nil {
		t.Fatalf("resolve pre-push hook path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	// A temp git repo with a committed HEAD on a named branch and an https
	// github.com origin, so the hook can derive repo root + branch + slug.
	newRepo := func(home string) string {
		repoDir := t.TempDir()
		env := isolatedGitEnv(home)
		mustRunEnv(t, repoDir, env, "git", "init", "-q")
		mustRunEnv(t, repoDir, env, "git", "config", "user.email", "a@b.c")
		mustRunEnv(t, repoDir, env, "git", "config", "user.name", "tester")
		mustRunEnv(t, repoDir, env, "git", "checkout", "-q", "-b", "feature-x")
		mustRunEnv(t, repoDir, env, "git", "remote", "add", "origin", "https://github.com/romaine-life/tank-operator.git")
		if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("hi"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		mustRunEnv(t, repoDir, env, "git", "add", "f.txt")
		mustRunEnv(t, repoDir, env, "git", "commit", "-q", "-m", "init")
		return repoDir
	}

	// runHook runs the pre-push hook in repoDir with the break-glass mint + push
	// endpoints pointed at the test servers, returning combined output + exit code.
	runHook := func(repoDir, home, mintURL, pushURL string) (string, int) {
		cmd := exec.Command("sh", hookSrc, "origin", "https://github.com/romaine-life/tank-operator.git")
		cmd.Dir = repoDir
		cmd.Env = append(isolatedGitEnv(home),
			"TANK_BREAK_GLASS_MINT_URL="+mintURL,
			"TANK_BREAK_GLASS_PUSH_HEAD_URL="+pushURL,
			"AUTH_ROMAINE_TOKEN_PATH="+tokenFile,
		)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("hook run errored unexpectedly: %v\noutput:\n%s", err, string(out))
		}
		return string(out), code
	}

	t.Run("scoped grant brokers the push and blocks git", func(t *testing.T) {
		home := t.TempDir()
		repoDir := newRepo(home)
		// Unlimited probe: no token (scoped grant, not unlimited).
		mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":false}`))
		}))
		defer mint.Close()
		var pushBody string
		push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			pushBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"branch":"feature-x","sha":"abc1234","repo":"romaine-life/tank-operator"}`))
		}))
		defer push.Close()

		out, code := runHook(repoDir, home, mint.URL, push.URL)
		if code == 0 {
			t.Fatalf("hook must exit non-zero to stop git's own push; got 0\noutput:\n%s", out)
		}
		if !strings.Contains(out, "Push SUCCEEDED via Tank's governed branch-lane path") {
			t.Fatalf("hook did not report the governed push success:\n%s", out)
		}
		if !strings.Contains(out, "EXPECTED") {
			t.Fatalf("hook did not explain the expected git failure line:\n%s", out)
		}
		if !strings.Contains(out, "feature-x") || !strings.Contains(out, "abc1234") {
			t.Fatalf("hook output missing branch/sha echo:\n%s", out)
		}
		if !strings.Contains(pushBody, "\"repo\":\"romaine-life/tank-operator\"") {
			t.Fatalf("/push-head body missing repo slug: %s", pushBody)
		}
		if !strings.Contains(pushBody, "\"repo_path\":\""+repoDir+"\"") {
			t.Fatalf("/push-head body missing repo_path: %s", pushBody)
		}
		// The preserved no-grant block must NOT appear on the governed-push path.
		if strings.Contains(out, "Direct git push is disabled in Tank normal mode") {
			t.Fatalf("governed push path should not print the no-grant block:\n%s", out)
		}
	})

	t.Run("branch out of scope blocks with explanation", func(t *testing.T) {
		home := t.TempDir()
		repoDir := newRepo(home)
		mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":false}`))
		}))
		defer mint.Close()
		push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":false,"reason":"branch_out_of_scope","detail":"feature-x not in scope"}`))
		}))
		defer push.Close()

		out, code := runHook(repoDir, home, mint.URL, push.URL)
		if code == 0 {
			t.Fatalf("out-of-scope push must exit non-zero; got 0\noutput:\n%s", out)
		}
		if !strings.Contains(out, "does not\ncover this branch") && !strings.Contains(out, "does not cover this branch") {
			t.Fatalf("hook did not explain the out-of-scope refusal:\n%s", out)
		}
	})

	t.Run("no grant falls through to preserved block", func(t *testing.T) {
		home := t.TempDir()
		repoDir := newRepo(home)
		mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"active":false}`))
		}))
		defer mint.Close()
		push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":false,"reason":"no_grant"}`))
		}))
		defer push.Close()

		out, code := runHook(repoDir, home, mint.URL, push.URL)
		if code != 1 {
			t.Fatalf("no-grant push must exit 1; got %d\noutput:\n%s", code, out)
		}
		if !strings.Contains(out, "[tank-agent] Direct git push is disabled") {
			t.Fatalf("no-grant path must preserve the historical block:\n%s", out)
		}
	})
}

// When an unlimited break-glass grant is active, the pre-push hook detects it
// via /mint-git-token and exits 0 so git's native push proceeds with the full
// token the credential helper provides. /push-head must not be consulted.
func TestAgentPrePushHookUnlimitedGrantAllowsNativePush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pre-push hook script test runs on POSIX only")
	}
	hookSrc, err := filepath.Abs("../../../k8s/session-config/agent-pre-push-hook.sh")
	if err != nil {
		t.Fatalf("resolve pre-push hook path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	home := t.TempDir()
	repoDir := t.TempDir()
	env := isolatedGitEnv(home)
	mustRunEnv(t, repoDir, env, "git", "init", "-q")
	mustRunEnv(t, repoDir, env, "git", "config", "user.email", "a@b.c")
	mustRunEnv(t, repoDir, env, "git", "config", "user.name", "tester")
	mustRunEnv(t, repoDir, env, "git", "checkout", "-q", "-b", "feature-x")
	mustRunEnv(t, repoDir, env, "git", "remote", "add", "origin", "https://github.com/romaine-life/tank-operator.git")
	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mustRunEnv(t, repoDir, env, "git", "add", "f.txt")
	mustRunEnv(t, repoDir, env, "git", "commit", "-q", "-m", "init")

	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"token":"ghs_fulltoken"}`))
	}))
	defer mint.Close()
	pushHit := false
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushHit = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer push.Close()

	cmd := exec.Command("sh", hookSrc, "origin", "https://github.com/romaine-life/tank-operator.git")
	cmd.Dir = repoDir
	cmd.Env = append(isolatedGitEnv(home),
		"TANK_BREAK_GLASS_MINT_URL="+mint.URL,
		"TANK_BREAK_GLASS_PUSH_HEAD_URL="+push.URL,
		"AUTH_ROMAINE_TOKEN_PATH="+tokenFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unlimited-grant pre-push hook must exit 0; got error %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "unlimited break-glass grant active") {
		t.Fatalf("hook did not report the unlimited grant:\n%s", string(out))
	}
	if pushHit {
		t.Fatalf("/push-head must not be consulted when an unlimited grant is active")
	}
}

// In restricted mode with a scoped (non-unlimited) grant, `gh pr comment <n>
// --body x` routes to the governed /pr-write endpoint and prints the returned
// pr_url. The unlimited /mint-git-token probe returns no token (so we do not
// exec real gh early), and the read-only mint is never reached because the
// PR-write succeeds and the wrapper exits 0.
func TestGhTankWrapperPrWriteComment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh wrapper script test runs on POSIX only")
	}
	wrapperSrc, err := filepath.Abs("../../../session-images/gh-tank-wrapper.sh")
	if err != nil {
		t.Fatalf("resolve gh wrapper path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	// Fake real gh: if reached, it would print a sentinel. The PR-write path must
	// NOT fall through to it on success.
	fakeGh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fakeGh, []byte("#!/bin/sh\nprintf 'NATIVE_GH_REACHED\\n'\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	emptyWorkspace := t.TempDir()

	// Unlimited probe returns no token -> scoped path.
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":false}`))
	}))
	defer mint.Close()
	var prBody string
	prWrite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		prBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"pr_number":42,"pr_url":"https://github.com/romaine-life/tank-operator/pull/42","action":"comment"}`))
	}))
	defer prWrite.Close()
	// Read-only mint must not be hit on a successful PR write.
	roHit := false
	ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		roHit = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ro.Close()

	cmd := exec.Command("sh", wrapperSrc, "pr", "comment", "42", "--repo", "romaine-life/tank-operator", "--body", "hello from tank")
	cmd.Env = []string{
		"TANK_RESTRICTED_GIT=true",
		"TANK_REAL_GH=" + fakeGh,
		"TANK_BREAK_GLASS_MINT_URL=" + mint.URL,
		"TANK_BREAK_GLASS_PR_WRITE_URL=" + prWrite.URL,
		"TANK_GIT_CRED_MCP_URL=" + ro.URL,
		"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
		"TANK_WORKSPACE_DIR=" + emptyWorkspace,
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "https://github.com/romaine-life/tank-operator/pull/42") {
		t.Fatalf("wrapper did not print the pr_url:\n%s", string(out))
	}
	if strings.Contains(string(out), "NATIVE_GH_REACHED") {
		t.Fatalf("PR-write success must not fall through to native gh:\n%s", string(out))
	}
	if roHit {
		t.Fatalf("read-only mint must not be hit on a successful PR write")
	}
	if !strings.Contains(prBody, "\"action\":\"comment\"") {
		t.Fatalf("/pr-write body wrong action: %s", prBody)
	}
	if !strings.Contains(prBody, "\"pr_number\":42") {
		t.Fatalf("/pr-write body missing pr_number: %s", prBody)
	}
	if !strings.Contains(prBody, "\"comment\":\"hello from tank\"") {
		t.Fatalf("/pr-write body did not map --body to comment: %s", prBody)
	}
	if !strings.Contains(prBody, "\"repo\":\"romaine-life/tank-operator\"") {
		t.Fatalf("/pr-write body missing repo: %s", prBody)
	}
}

// A `gh pr comment` whose PR write returns no_grant must name
// request_git_break_glass on stderr and then fall through to native gh so the
// user also sees gh's own result.
func TestGhTankWrapperPrWriteNoGrantFallsThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh wrapper script test runs on POSIX only")
	}
	wrapperSrc, err := filepath.Abs("../../../session-images/gh-tank-wrapper.sh")
	if err != nil {
		t.Fatalf("resolve gh wrapper path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	fakeGh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fakeGh, []byte("#!/bin/sh\nprintf 'NATIVE_GH GH_TOKEN=%s\\n' \"$GH_TOKEN\"\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	emptyWorkspace := t.TempDir()

	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":false}`))
	}))
	defer mint.Close()
	prWrite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"reason":"no_grant"}`))
	}))
	defer prWrite.Close()
	// Read-only mint IS reached on the fall-through, so it must still serve a token.
	ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_readonly\"}}}\n\n"))
	}))
	defer ro.Close()

	cmd := exec.Command("sh", wrapperSrc, "pr", "comment", "42", "--repo", "romaine-life/tank-operator", "--body", "x")
	cmd.Env = []string{
		"TANK_RESTRICTED_GIT=true",
		"TANK_REAL_GH=" + fakeGh,
		"TANK_BREAK_GLASS_MINT_URL=" + mint.URL,
		"TANK_BREAK_GLASS_PR_WRITE_URL=" + prWrite.URL,
		"TANK_GIT_CRED_MCP_URL=" + ro.URL,
		"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
		"TANK_WORKSPACE_DIR=" + emptyWorkspace,
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "request_git_break_glass") {
		t.Fatalf("no_grant PR write must name request_git_break_glass:\n%s", string(out))
	}
	if !strings.Contains(string(out), "NATIVE_GH GH_TOKEN=ghs_readonly") {
		t.Fatalf("no_grant PR write must fall through to native gh with the read-only token:\n%s", string(out))
	}
}

// `gh pr view` (a READ subcommand) must never touch /pr-write — it stays on the
// existing read-only mint + native gh path, even in restricted mode.
func TestGhTankWrapperPrReadNotIntercepted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gh wrapper script test runs on POSIX only")
	}
	wrapperSrc, err := filepath.Abs("../../../session-images/gh-tank-wrapper.sh")
	if err != nil {
		t.Fatalf("resolve gh wrapper path: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(tokenFile, []byte("fake-sa-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	fakeGh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fakeGh, []byte("#!/bin/sh\nprintf 'NATIVE_GH GH_TOKEN=%s\\n' \"$GH_TOKEN\"\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	emptyWorkspace := t.TempDir()

	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":false}`))
	}))
	defer mint.Close()
	prWriteHit := false
	prWrite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prWriteHit = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer prWrite.Close()
	ro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"structuredContent\":{\"token\":\"ghs_readonly\"}}}\n\n"))
	}))
	defer ro.Close()

	cmd := exec.Command("sh", wrapperSrc, "pr", "view", "42", "--repo", "romaine-life/tank-operator")
	cmd.Env = []string{
		"TANK_RESTRICTED_GIT=true",
		"TANK_REAL_GH=" + fakeGh,
		"TANK_BREAK_GLASS_MINT_URL=" + mint.URL,
		"TANK_BREAK_GLASS_PR_WRITE_URL=" + prWrite.URL,
		"TANK_GIT_CRED_MCP_URL=" + ro.URL,
		"AUTH_ROMAINE_TOKEN_PATH=" + tokenFile,
		"TANK_WORKSPACE_DIR=" + emptyWorkspace,
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\noutput:\n%s", err, string(out))
	}
	if prWriteHit {
		t.Fatalf("`gh pr view` (a read) must not hit /pr-write")
	}
	if !strings.Contains(string(out), "NATIVE_GH GH_TOKEN=ghs_readonly") {
		t.Fatalf("`gh pr view` must reach native gh with the read-only token:\n%s", string(out))
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
	skillsScript, err := filepath.Abs("../../../k8s/session-config/install-tank-skills.sh")
	if err != nil {
		t.Fatalf("resolve skills install script path: %v", err)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("script not at expected path %s: %v", scriptPath, err)
	}
	skillsConfigDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillsConfigDir, "skills__common__orchestrate__SKILL.md"), []byte("simple orchestrate"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsConfigDir, "skills__common__orchestrate__agents__openai.yaml"), []byte("orchestrate agent"), 0o644); err != nil {
		t.Fatal(err)
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
				"INSTALL_TANK_SKILLS_SCRIPT="+skillsScript,
				"INSTALL_TANK_SKILLS_CONFIG_DIR="+skillsConfigDir,
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
			assertFileContains(t, filepath.Join(home, ".claude", "skills", "orchestrate", "SKILL.md"), "simple orchestrate")
			assertFileContains(t, filepath.Join(home, ".codex", "skills", "orchestrate", "SKILL.md"), "simple orchestrate")
			assertFileContains(t, filepath.Join(home, ".claude", "skills", "orchestrate", "agents", "openai.yaml"), "orchestrate agent")
			assertFileContains(t, filepath.Join(home, ".codex", "skills", "orchestrate", "agents", "openai.yaml"), "orchestrate agent")

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
