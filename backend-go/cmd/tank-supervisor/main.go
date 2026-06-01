package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultChildCommand = "/app/tank-operator-go"
	defaultStopTimeout  = 10 * time.Second
)

type config struct {
	childCommand   string
	hotArtifact    string
	restartEnabled bool
	stopTimeout    time.Duration
}

type childProcess struct {
	cmd        *exec.Cmd
	generation int
	exited     chan childExit
}

type childExit struct {
	generation int
	err        error
}

func main() {
	os.Exit(run(loadConfig()))
}

func loadConfig() config {
	return config{
		childCommand:   firstNonEmpty(os.Getenv("GLIMMUNG_SUPERVISOR_CHILD"), defaultChildCommand),
		hotArtifact:    strings.TrimSpace(os.Getenv("GLIMMUNG_SUPERVISOR_HOT_ARTIFACT")),
		restartEnabled: envBoolDefault("GLIMMUNG_SUPERVISOR_RESTART_ENABLED", true),
		stopTimeout:    envDurationDefault("GLIMMUNG_SUPERVISOR_STOP_TIMEOUT_SECONDS", defaultStopTimeout),
	}
}

func run(cfg config) int {
	child, err := startSupervisedChild(cfg, 1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start child: %v\n", err)
		return 1
	}

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	for {
		select {
		case sig := <-signals:
			switch sig {
			case syscall.SIGHUP:
				if !cfg.restartEnabled {
					continue
				}
				if err := stopChild(child, cfg.stopTimeout); err != nil {
					if !isChildExitStatus(err) {
						fmt.Fprintf(os.Stderr, "stop child for restart: %v\n", err)
					}
				}
				child, err = startSupervisedChild(cfg, child.generation+1)
				if err != nil {
					fmt.Fprintf(os.Stderr, "restart child: %v\n", err)
					return 1
				}
			case syscall.SIGTERM, syscall.SIGINT:
				if err := stopChild(child, cfg.stopTimeout); err != nil {
					fmt.Fprintf(os.Stderr, "stop child: %v\n", err)
					return exitCode(err)
				}
				return 0
			}
		case result := <-child.exited:
			if result.generation != child.generation {
				fmt.Fprintf(os.Stderr, "ignoring stale child exit generation=%d current=%d err=%v\n", result.generation, child.generation, result.err)
				continue
			}
			return exitCode(result.err)
		}
	}
}

func startSupervisedChild(cfg config, generation int) (*childProcess, error) {
	cmd, err := startChild(cfg)
	if err != nil {
		return nil, err
	}
	child := &childProcess{
		cmd:        cmd,
		generation: generation,
		exited:     make(chan childExit, 1),
	}
	go func(cmd *exec.Cmd, generation int, exited chan<- childExit) {
		exited <- childExit{generation: generation, err: cmd.Wait()}
	}(cmd, generation, child.exited)
	return child, nil
}

func startChild(cfg config) (*exec.Cmd, error) {
	command := cfg.childCommand
	if cfg.hotArtifact != "" {
		if info, err := os.Stat(cfg.hotArtifact); err == nil && !info.IsDir() {
			command = cfg.hotArtifact
		}
	}
	cmd := exec.Command(command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	configureChildProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "tank-supervisor started pid=%d command=%s\n", cmd.Process.Pid, command)
	return cmd, nil
}

func stopChild(child *childProcess, timeout time.Duration) error {
	if child == nil || child.cmd == nil || child.cmd.Process == nil {
		return nil
	}
	if err := signalChildTree(child.cmd, syscall.SIGTERM); err != nil {
		select {
		case result := <-child.exited:
			return result.err
		default:
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case result := <-child.exited:
		return result.err
	case <-ctx.Done():
		_ = signalChildTree(child.cmd, syscall.SIGKILL)
		<-child.exited
		return ctx.Err()
	}
}

func isChildExitStatus(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "child wait failed: %v\n", err)
	return 1
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func envBoolDefault(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDurationDefault(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
