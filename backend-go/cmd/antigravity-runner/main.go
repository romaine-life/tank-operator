package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/romaine-life/tank-operator/backend-go/internal/sessionbus"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		slog.Info("Shutting down antigravity-runner...")
		cancel()
	}()

	slog.Info("Starting antigravity-runner spike")

	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://tank-nats.nats.svc.cluster.local:4222"
	}
	sessionStorageKey := os.Getenv("SESSION_STORAGE_KEY")
	if sessionStorageKey == "" {
		slog.Warn("SESSION_STORAGE_KEY not set, using default")
		sessionStorageKey = "default:dev-session"
	}
	provider := "antigravity"

	nc, err := nats.Connect(natsURL)
	if err != nil {
		slog.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("Failed to create JetStream", "error", err)
		os.Exit(1)
	}

	// 1. Start Subprocess
	cmd := exec.CommandContext(ctx, "antigravity")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		slog.Error("Failed to get stdin pipe", "error", err)
		os.Exit(1)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("Failed to get stdout pipe", "error", err)
		os.Exit(1)
	}

	if err := cmd.Start(); err != nil {
		slog.Error("Failed to start antigravity subprocess", "error", err)
		os.Exit(1)
	}

	// 2. Start Event Publisher (stdout -> NATS)
	go func() {
		scanner := bufio.NewScanner(stdout)
		eventSubject := sessionbus.SessionEventSubject(sessionStorageKey)
		for scanner.Scan() {
			line := scanner.Text()
			// Forward JSONL to events subject
			if err := nc.Publish(eventSubject, []byte(line)); err != nil {
				slog.Error("Failed to publish event", "error", err)
			}
		}
	}()

	// 3. Start Data-Plane Consumer
	go func() {
		commandSubject := sessionbus.CommandSubject(sessionStorageKey, provider)
		consumerName := "antigravity_data_" + sessionbus.StorageToken(sessionStorageKey)
		
		consumer, err := js.CreateOrUpdateConsumer(ctx, "TANK_SESSION_BUS", jetstream.ConsumerConfig{
			Durable:       consumerName,
			Name:          consumerName,
			FilterSubject: commandSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       120 * time.Second,
			MaxDeliver:    20,
			MaxAckPending: 1, // Serial dispatch
		})
		if err != nil {
			slog.Error("Failed to create data-plane consumer", "error", err)
			return
		}

		cctx, _ := consumer.Consume(func(msg jetstream.Msg) {
			var command sessionbus.Command
			if err := json.Unmarshal(msg.Data(), &command); err != nil {
				msg.TermWithReason("invalid json")
				return
			}
			slog.Info("Received data-plane command", "type", command.Type)
			
			if command.Type == sessionbus.CommandSubmitTurn {
				// Send to CLI stdin
				payload, _ := json.Marshal(map[string]string{"prompt": command.Prompt})
				stdin.Write(append(payload, '\n'))
				
				// In a full implementation, we'd wait for turn completion to ack.
				// For the spike, we ack immediately to free the consumer.
				msg.Ack()
			} else {
				msg.Ack()
			}
		})
		<-ctx.Done()
		cctx.Stop()
	}()

	// 4. Start Control-Plane Consumer
	go func() {
		controlSubject := sessionbus.ControlSubject(sessionStorageKey, provider)
		consumerName := "antigravity_control_" + sessionbus.StorageToken(sessionStorageKey)
		
		consumer, err := js.CreateOrUpdateConsumer(ctx, "TANK_SESSION_BUS", jetstream.ConsumerConfig{
			Durable:       consumerName,
			Name:          consumerName,
			FilterSubject: controlSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       15 * time.Second,
			MaxDeliver:    10,
			MaxAckPending: 16, // Burst control
		})
		if err != nil {
			slog.Error("Failed to create control-plane consumer", "error", err)
			return
		}

		cctx, _ := consumer.Consume(func(msg jetstream.Msg) {
			var command sessionbus.Command
			if err := json.Unmarshal(msg.Data(), &command); err != nil {
				msg.TermWithReason("invalid json")
				return
			}
			slog.Info("Received control-plane command", "type", command.Type)
			
			if command.Type == sessionbus.CommandInterrupt {
				// Kill subprocess or send interrupt signal
				slog.Info("Interrupting antigravity subprocess")
				cmd.Process.Signal(os.Interrupt)
			}
			msg.Ack()
		})
		<-ctx.Done()
		cctx.Stop()
	}()

	cmd.Wait()
	slog.Info("Antigravity-runner exited naturally")
}

