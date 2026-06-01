package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestStartSupervisedChildReportsOwnGeneration(t *testing.T) {
	first, err := startSupervisedChild(testCommand(t, "exit 7"), 41)
	if err != nil {
		t.Fatalf("start first child: %v", err)
	}
	second, err := startSupervisedChild(testCommand(t, "exit 9"), 42)
	if err != nil {
		t.Fatalf("start second child: %v", err)
	}

	firstExit := <-first.exited
	secondExit := <-second.exited

	if firstExit.generation != 41 {
		t.Fatalf("first generation = %d, want 41", firstExit.generation)
	}
	if secondExit.generation != 42 {
		t.Fatalf("second generation = %d, want 42", secondExit.generation)
	}
	if got := exitCode(firstExit.err); got != 7 {
		t.Fatalf("first exit code = %d, want 7", got)
	}
	if got := exitCode(secondExit.err); got != 9 {
		t.Fatalf("second exit code = %d, want 9", got)
	}
}

func TestStopChildTerminatesAndReapsProcess(t *testing.T) {
	child, err := startSupervisedChild(testCommand(t, "sleep 30"), 42)
	if err != nil {
		t.Fatalf("start child: %v", err)
	}

	if err := stopChild(child, 2*time.Second); err == nil {
		t.Fatal("stop child = nil, want signal exit")
	}
	if err := child.cmd.Process.Signal(syscall.Signal(0)); err == nil {
		t.Fatal("process still accepts signal after wait")
	}
}

func testCommand(t *testing.T, body string) config {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("tank-supervisor signal tests are Unix-only")
	}
	path := filepath.Join(t.TempDir(), "child.sh")
	content := "#!/bin/bash\nset -e\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write child script: %v", err)
	}
	return config{childCommand: path, restartEnabled: true}
}
