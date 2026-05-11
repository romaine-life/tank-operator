package sessions

import (
	"context"
	"fmt"
	"strings"

	"github.com/nelsong6/tank-operator/backend-go/internal/auth"
	"github.com/nelsong6/tank-operator/backend-go/internal/compat"
	"github.com/nelsong6/tank-operator/backend-go/internal/kubeexec"
	"github.com/nelsong6/tank-operator/backend-go/internal/store"
)

// DispatchParams holds all the parameters for a headless dispatch.
type DispatchParams struct {
	Namespace      string
	Email          string
	SessionID      string
	Prompt         string
	FollowUp       bool
	Model          string // already validated against [A-Za-z0-9._-]{1,64}
	PermissionMode string // already validated against [A-Za-z0-9._-]{1,64}
	SkillName      string // already validated against [A-Za-z0-9_-]{1,64}
	ActiveRuns     store.ActiveRunStore // may be nil
	Events         *EventBus
}

// DispatchHeadless fires a headless run on the session pod without waiting for completion.
func (m *Manager) DispatchHeadless(ctx context.Context, p DispatchParams) error {
	namespace := p.Namespace
	if namespace == "" {
		namespace = m.namespace
	}

	podName, err := m.GetPodName(ctx, p.Email, p.SessionID)
	if err != nil {
		return fmt.Errorf("dispatch: pod not ready: %w", err)
	}

	// Validate mode is headless.
	info, err := m.GetByOwner(ctx, p.Email, p.SessionID)
	if err != nil {
		return fmt.Errorf("dispatch: get session: %w", err)
	}
	mode := info.Mode
	if mode != compat.ClaudeGUIMode && mode != compat.CodexGUIMode {
		return fmt.Errorf("dispatch: session mode %q is not headless (need claude_gui or codex_gui)", mode)
	}

	provider := "claude"
	if mode == compat.CodexGUIMode {
		provider = "codex"
	}

	runID := auth.RandomHex(12)
	promptPath := "/tmp/tank-prompt-" + auth.RandomHex(8)
	streamPath := compat.RunStreamPath(runID)
	pidPath := compat.RunPIDPath(runID)

	// Write prompt to pod.
	if err := kubeexec.WriteFile(ctx, m.client, m.restCfg, namespace, podName, promptPath, []byte(p.Prompt)); err != nil {
		return fmt.Errorf("dispatch: write prompt: %w", err)
	}

	// Build the inner headless command.
	followUpStr := "false"
	if p.FollowUp {
		followUpStr = "true"
	}
	headlessCmd := fmt.Sprintf(
		"bash /opt/tank/headless-run.sh %s %s %s %s %s %s",
		shellQ(provider),
		shellQ(promptPath),
		followUpStr,
		shellQ(p.Model),
		shellQ(p.PermissionMode),
		shellQ(p.SkillName),
	)

	// Build the live run wrapper script.
	liveScript := buildLiveRunScript(pidPath, headlessCmd)

	if err := kubeexec.LaunchDetached(ctx, m.client, m.restCfg, namespace, podName, liveScript, streamPath); err != nil {
		return fmt.Errorf("dispatch: launch detached: %w", err)
	}

	if p.ActiveRuns != nil {
		_, _ = p.ActiveRuns.Start(ctx, p.Email, p.SessionID, runID, podName, provider, streamPath, pidPath)
	}
	if p.Events != nil {
		p.Events.Publish(p.Email)
	}

	return nil
}

// buildLiveRunScript builds the full bash wrapper that writes PID, sets up traps,
// runs the inner command, and writes the exit marker.
func buildLiveRunScript(pidPath, innerCmd string) string {
	qPid := shellQ(pidPath)
	var b strings.Builder
	b.WriteString("echo $$ > ")
	b.WriteString(qPid)
	b.WriteString("; ")
	b.WriteString("trap 'rc=$?; rm -f ")
	b.WriteString(qPid)
	b.WriteString("; printf \"\\n__TANK_RUN_EXIT__:%s\\n\" \"$rc\"; exit $rc' TERM INT; ")
	b.WriteString(innerCmd)
	b.WriteString("; rc=$?; rm -f ")
	b.WriteString(qPid)
	b.WriteString("; printf '\\n__TANK_RUN_EXIT__:%s\\n' \"$rc\"; exit $rc")
	return b.String()
}

// shellQ single-quotes a string with proper escaping.
func shellQ(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// BuildLiveRunScript wraps the inner command with PID tracking and exit marker.
// innerCmd is the full command string to run; pidPath is where to record the PID.
func BuildLiveRunScript(innerCmd, pidPath string) string {
	return buildLiveRunScript(pidPath, innerCmd)
}

// BuildTailRunScript builds a script that tails a stream file until the exit marker.
func BuildTailRunScript(streamPath string, offset int) string {
	quotedPath := shellQ(streamPath)
	marker := "__TANK_RUN_EXIT__:"
	startByte := offset + 1
	if startByte < 1 {
		startByte = 1
	}
	return fmt.Sprintf(
		"set -euo pipefail; "+
			"deadline=$((SECONDS+30)); "+
			"while [ ! -f %s ]; do sleep 0.2; [ $SECONDS -lt $deadline ] || { echo 'timed out waiting for run stream' >&2; exit 1; }; done; "+
			"tail -c +%d -F %s & tail_pid=$!; "+
			"while [ -f %s ] && ! grep -q '^%s' %s; do sleep 0.5; done; "+
			"sleep 0.2; kill \"$tail_pid\" 2>/dev/null || true; wait \"$tail_pid\" 2>/dev/null || true; "+
			"rc=$(sed -n 's/^%s//p' %s 2>/dev/null | tail -1) || rc=; "+
			"rm -f %s; exit \"${rc:-0}\"",
		quotedPath,
		startByte, quotedPath,
		quotedPath, marker, quotedPath,
		marker, quotedPath,
		quotedPath,
	)
}

// BuildCancelRunCommand returns a command to SIGTERM the process recorded in pidPath.
func BuildCancelRunCommand(pidPath string) []string {
	qPid := shellQ(pidPath)
	return []string{"bash", "-lc",
		"pid=$(cat " + qPid + " 2>/dev/null || true); " +
			"if [ -n \"$pid\" ]; then " +
			"pkill -TERM -P \"$pid\" 2>/dev/null || true; " +
			"kill -TERM \"$pid\" 2>/dev/null || true; " +
			"fi",
	}
}
