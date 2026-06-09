// Package kubeexec provides Kubernetes pod exec helpers for session pods.
// Session pods are multi-container; all exec calls target container="sandbox".
package kubeexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/coder/websocket"
)

const (
	sessionContainer       = "sandbox"
	detachedLaunchAttempts = 3
	keepaliveInterval      = 30 * time.Second
)

var detachedLaunchDelays = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// Capture runs a one-shot command in the session container and returns stdout.
func Capture(ctx context.Context, k8s kubernetes.Interface, cfg *rest.Config, namespace, podName string, command []string) ([]byte, error) {
	exec, err := newExecutor(k8s, cfg, namespace, podName, command, false)
	if err != nil {
		return nil, fmt.Errorf("exec %v: %w", command, err)
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if stderr.Len() > 0 {
		slog.Warn("exec stderr", "command", command, "stderr", stderr.String()[:min(stderr.Len(), 500)])
	}
	if err != nil {
		return nil, fmt.Errorf("exec %v: %w", command, err)
	}
	return stdout.Bytes(), nil
}

// CaptureWithStdin runs a one-shot command in the session container,
// pipes stdinData to its stdin, and returns its stdout. Use when the
// payload bytes and the placement decision must happen inside a single
// exec — e.g. server-side id allocation for screenshot uploads, where
// the script picks the next free `screenshots/<n>.<ext>` path with
// O_EXCL and writes the bytes in the same call so two concurrent
// uploads can't pick the same id.
func CaptureWithStdin(ctx context.Context, k8s kubernetes.Interface, cfg *rest.Config, namespace, podName string, command []string, stdinData []byte) ([]byte, error) {
	exec, err := newExecutor(k8s, cfg, namespace, podName, command, true)
	if err != nil {
		return nil, fmt.Errorf("exec %v: %w", command, err)
	}
	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  bytes.NewReader(stdinData),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if stderr.Len() > 0 {
		slog.Warn("exec stderr", "command", command, "stderr", stderr.String()[:min(stderr.Len(), 500)])
	}
	if err != nil {
		return nil, fmt.Errorf("exec %v: %w", command, err)
	}
	return stdout.Bytes(), nil
}

// WriteFile writes data to path inside the session container.
func WriteFile(ctx context.Context, k8s kubernetes.Interface, cfg *rest.Config, namespace, podName, filePath string, data []byte) error {
	quotedPath := shellQuote(filePath)
	quotedDir := shellQuote(path.Dir(filePath))
	command := []string{
		"bash", "-lc",
		fmt.Sprintf("set -euo pipefail; mkdir -p %s; umask 077; head -c %d > %s", quotedDir, len(data), quotedPath),
	}
	exec, err := newExecutor(k8s, cfg, namespace, podName, command, true)
	if err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	var stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  bytes.NewReader(data),
		Stdout: io.Discard,
		Stderr: &stderr,
	})
	if stderr.Len() > 0 {
		slog.Warn("write file stderr", "path", filePath, "stderr", stderr.String()[:min(stderr.Len(), 500)])
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	return nil
}

// LaunchDetached spawns command on the pod and returns immediately.
func LaunchDetached(ctx context.Context, k8s kubernetes.Interface, cfg *rest.Config, namespace, podName, command, logPath string) error {
	launcher := buildDetachedLauncher(command, logPath)
	cmd := []string{"bash", "-lc", launcher}
	var lastErr error
	for attempt := 0; attempt < detachedLaunchAttempts; attempt++ {
		_, err := Capture(ctx, k8s, cfg, namespace, podName, cmd)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < len(detachedLaunchDelays) {
			slog.Warn("detached launch retry", "pod", podName, "attempt", attempt+1, "err", err)
			time.Sleep(detachedLaunchDelays[attempt])
		}
	}
	return fmt.Errorf("detached launch failed: %w", lastErr)
}

// StdoutObserver receives stdout text chunks as they arrive.
type StdoutObserver func(text string)

// StreamToWebSocket runs command in the session container and bridges output
// to a browser WebSocket. Protocol matches the Python exec_stream_to_websocket.
//
// server→browser: {"stream":"stdout"|"stderr","data":"..."}, {"keepalive":true},
//
//	{"status":"done"}, {"status":"error","detail":"..."}
//
// browser→server: {"stdin":"..."}, {"cancel":true}
//
// A browser disconnect (tab reload) does NOT cancel the pod command.
// An explicit {"cancel":true} frame does cancel via cancelCommand.
func StreamToWebSocket(
	ctx context.Context,
	k8s kubernetes.Interface,
	cfg *rest.Config,
	namespace, podName string,
	command []string,
	initialStdin []byte,
	cancelCommand []string,
	browser *websocket.Conn,
	observer StdoutObserver,
) error {
	stdinR, stdinW := io.Pipe()

	if len(initialStdin) > 0 {
		go func() {
			_, _ = stdinW.Write(initialStdin)
		}()
	}

	exec, err := newExecutor(k8s, cfg, namespace, podName, command, len(initialStdin) > 0)
	if err != nil {
		_ = sendBrowserJSON(ctx, browser, map[string]any{"status": "error", "detail": err.Error()})
		browser.Close(websocket.StatusInternalError, "")
		return err
	}

	stdoutW := &jsonWriter{stream: "stdout", conn: browser, ctx: ctx, observer: observer}
	stderrW := &jsonWriter{stream: "stderr", conn: browser, ctx: ctx}

	podCtx, podCancel := context.WithCancel(ctx)
	defer podCancel()

	podDone := make(chan error, 1)
	go func() {
		podDone <- exec.StreamWithContext(podCtx, remotecommand.StreamOptions{
			Stdin:  stdinR,
			Stdout: stdoutW,
			Stderr: stderrW,
		})
		stdinW.Close()
	}()

	stdinCh := make(chan []byte, 10)
	cancelCh := make(chan struct{}, 1)
	disconnectCh := make(chan struct{}, 1)

	go func() {
		for {
			_, data, err := browser.Read(ctx)
			if err != nil {
				select {
				case disconnectCh <- struct{}{}:
				default:
				}
				return
			}
			var msg map[string]any
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			if s, _ := msg["stdin"].(string); s != "" {
				select {
				case stdinCh <- []byte(s):
				default:
				}
			}
			if c, _ := msg["cancel"].(bool); c {
				select {
				case cancelCh <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	browserDead := false

	for {
		select {
		case podErr := <-podDone:
			if !browserDead {
				if podErr != nil {
					_ = sendBrowserJSON(ctx, browser, map[string]any{"status": "error", "detail": podErr.Error()})
				} else {
					_ = sendBrowserJSON(ctx, browser, map[string]any{"status": "done"})
				}
				browser.Close(websocket.StatusNormalClosure, "")
			}
			return podErr

		case data := <-stdinCh:
			_, _ = stdinW.Write(data)

		case <-cancelCh:
			if len(cancelCommand) > 0 {
				go func() {
					_, _ = Capture(ctx, k8s, cfg, namespace, podName, cancelCommand)
				}()
			}
			podCancel()
			<-podDone
			return nil

		case <-disconnectCh:
			// Browser disconnected (tab reload): let pod keep running.
			browserDead = true
			<-podDone
			return nil

		case <-keepalive.C:
			if !browserDead {
				if err := sendBrowserJSON(ctx, browser, map[string]any{"keepalive": true}); err != nil {
					browserDead = true
				}
			}
		}
	}
}

// jsonWriter writes data as {"stream":"...","data":"..."} JSON frames to browser.
type jsonWriter struct {
	stream   string
	conn     *websocket.Conn
	ctx      context.Context
	observer StdoutObserver
}

func (w *jsonWriter) Write(p []byte) (int, error) {
	text := string(p)
	if w.stream == "stderr" && strings.Contains(text, "Warning: no stdin data received") {
		return len(p), nil
	}
	if w.observer != nil && w.stream == "stdout" {
		w.observer(text)
	}
	frame, _ := json.Marshal(map[string]string{"stream": w.stream, "data": text})
	_ = w.conn.Write(w.ctx, websocket.MessageText, frame)
	return len(p), nil
}

func sendBrowserJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	frame, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, frame)
}

func newExecutor(k8s kubernetes.Interface, cfg *rest.Config, namespace, podName string, command []string, stdin bool) (remotecommand.Executor, error) {
	req := k8s.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", sessionContainer).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("tty", "false")
	if stdin {
		req = req.Param("stdin", "true")
	}
	for _, c := range command {
		req = req.Param("command", c)
	}
	_ = corev1.Pod{} // ensure corev1 import is used
	return remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
}

func buildDetachedLauncher(command, logPath string) string {
	quotedLog := shellQuote(logPath)
	quotedCmd := shellQuote(command)
	return fmt.Sprintf(
		"set -uo pipefail; nohup bash -c %s >> %s 2>&1 < /dev/null & disown $! 2>/dev/null || true; echo launched",
		quotedCmd, quotedLog,
	)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
