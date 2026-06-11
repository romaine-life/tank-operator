package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const clusterHealthDescription = "Authenticated cluster-health snapshot for the Tank home/sidebar surface. It summarizes Kubernetes node readiness, Tank session pod readiness, and NATS JetStream pressure so cluster-level failure modes are visible without browser devtools."

type clusterHealthResponse struct {
	Description string                  `json:"description"`
	Status      string                  `json:"status"`
	CheckedAt   string                  `json:"checked_at"`
	Nodes       clusterNodeHealth       `json:"nodes"`
	Sessions    clusterSessionPodHealth `json:"sessions"`
	NATS        clusterNATSHealth       `json:"nats"`
	Upgrade     clusterUpgradeHealth    `json:"upgrade"`
}

type clusterNodeHealth struct {
	Status              string `json:"status"`
	Total               int    `json:"total"`
	Ready               int    `json:"ready"`
	NotReady            int    `json:"not_ready"`
	Unschedulable       int    `json:"unschedulable"`
	MemoryPressureNodes int    `json:"memory_pressure_nodes"`
	DiskPressureNodes   int    `json:"disk_pressure_nodes"`
	PIDPressureNodes    int    `json:"pid_pressure_nodes"`
	Error               string `json:"error,omitempty"`
}

type clusterSessionPodHealth struct {
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Running   int    `json:"running"`
	Pending   int    `json:"pending"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Unknown   int    `json:"unknown"`
	Ready     int    `json:"ready"`
	NotReady  int    `json:"not_ready"`
	Restarts  int32  `json:"restarts"`
	Error     string `json:"error,omitempty"`
}

type clusterNATSHealth struct {
	Status                string              `json:"status"`
	ConfiguredMonitorURLs int                 `json:"configured_monitor_urls"`
	ReachableServers      int                 `json:"reachable_servers"`
	ExpectedServers       int                 `json:"expected_servers"`
	Servers               []clusterNATSServer `json:"servers"`
	JetStream             clusterJetStream    `json:"jetstream"`
	Warnings              []string            `json:"warnings,omitempty"`
	Error                 string              `json:"error,omitempty"`
}

type clusterNATSServer struct {
	Name      string `json:"name,omitempty"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

type clusterJetStream struct {
	MemoryBytes             int64   `json:"memory_bytes"`
	MaxMemoryBytes          int64   `json:"max_memory_bytes"`
	MemoryUtilization       float64 `json:"memory_utilization"`
	ReservedMemoryBytes     int64   `json:"reserved_memory_bytes"`
	MetaPending             int64   `json:"meta_pending"`
	SlowConsumers           int64   `json:"slow_consumers"`
	Streams                 int64   `json:"streams"`
	Consumers               int64   `json:"consumers"`
	Messages                int64   `json:"messages"`
	Bytes                   int64   `json:"bytes"`
	StreamName              string  `json:"stream_name,omitempty"`
	StreamReplicas          int     `json:"stream_replicas"`
	ExpectedStreamReplicas  int     `json:"expected_stream_replicas"`
	StreamCurrentReplicas   int     `json:"stream_current_replicas"`
	StreamLaggingReplicas   int     `json:"stream_lagging_replicas"`
	streamReplicaLeaderView bool
	StreamMessages          int64 `json:"stream_messages"`
	StreamBytes             int64 `json:"stream_bytes"`
	StreamConsumers         int64 `json:"stream_consumers"`
}

type clusterUpgradeHealth struct {
	Status            string                       `json:"status"`
	State             string                       `json:"state"`
	Summary           string                       `json:"summary"`
	Signals           []string                     `json:"signals,omitempty"`
	Window            clusterUpgradeWindow         `json:"window"`
	NodeImageVersions []clusterUpgradeVersionCount `json:"node_image_versions,omitempty"`
	KubeletVersions   []clusterUpgradeVersionCount `json:"kubelet_versions,omitempty"`
}

type clusterUpgradeWindow struct {
	Configured              bool    `json:"configured"`
	Label                   string  `json:"label"`
	DayOfWeek               string  `json:"day_of_week"`
	StartTime               string  `json:"start_time"`
	UTCOffset               string  `json:"utc_offset"`
	DurationHours           float64 `json:"duration_hours"`
	Active                  bool    `json:"active"`
	CurrentWindowStartedAt  string  `json:"current_window_started_at,omitempty"`
	CurrentWindowEndsAt     string  `json:"current_window_ends_at,omitempty"`
	SecondsRemaining        int64   `json:"seconds_remaining,omitempty"`
	NextWindowStartsAt      string  `json:"next_window_starts_at,omitempty"`
	SecondsUntilNextWindow  int64   `json:"seconds_until_next_window,omitempty"`
	WindowComputationFailed string  `json:"error,omitempty"`
}

type clusterUpgradeVersionCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

func (s *appServer) handleClusterHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	writeJSON(w, http.StatusOK, s.clusterHealthSnapshot(ctx, time.Now().UTC()))
}

func (s *appServer) clusterHealthSnapshot(ctx context.Context, now time.Time) clusterHealthResponse {
	nodes := s.collectNodeHealth(ctx)
	sessions := s.collectSessionPodHealth(ctx)
	nats := collectNATSHealth(ctx, natsMonitorURLs(), envDefault("NATS_STREAM", "TANK_SESSION_BUS"), expectedNATSStreamReplicas())
	upgrade := s.collectUpgradeHealth(ctx, now)

	return clusterHealthResponse{
		Description: clusterHealthDescription,
		Status:      worstHealthStatus(nodes.Status, sessions.Status, nats.Status, upgrade.Status),
		CheckedAt:   now.Format(time.RFC3339Nano),
		Nodes:       nodes,
		Sessions:    sessions,
		NATS:        nats,
		Upgrade:     upgrade,
	}
}

func (s *appServer) collectNodeHealth(ctx context.Context) clusterNodeHealth {
	if s.k8s == nil {
		return clusterNodeHealth{Status: "unknown", Error: "kubernetes client not configured"}
	}
	nodes, err := s.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return clusterNodeHealth{Status: "unknown", Error: err.Error()}
	}
	out := clusterNodeHealth{Total: len(nodes.Items)}
	for _, node := range nodes.Items {
		if node.Spec.Unschedulable {
			out.Unschedulable++
		}
		if nodeConditionTrue(node, corev1.NodeReady) {
			out.Ready++
		} else {
			out.NotReady++
		}
		if nodeConditionTrue(node, corev1.NodeMemoryPressure) {
			out.MemoryPressureNodes++
		}
		if nodeConditionTrue(node, corev1.NodeDiskPressure) {
			out.DiskPressureNodes++
		}
		if nodeConditionTrue(node, corev1.NodePIDPressure) {
			out.PIDPressureNodes++
		}
	}
	switch {
	case out.Total == 0:
		out.Status = "unknown"
	case out.Ready == 0:
		out.Status = "critical"
	case out.NotReady > 0 || out.Unschedulable > 0 || out.MemoryPressureNodes > 0 || out.DiskPressureNodes > 0 || out.PIDPressureNodes > 0:
		out.Status = "warning"
	default:
		out.Status = "healthy"
	}
	return out
}

func (s *appServer) collectSessionPodHealth(ctx context.Context) clusterSessionPodHealth {
	if s.k8s == nil {
		return clusterSessionPodHealth{Status: "unknown", Error: "kubernetes client not configured"}
	}
	namespace := strings.TrimSpace(s.namespace)
	if namespace == "" {
		namespace = "tank-operator-sessions"
	}
	pods, err := s.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return clusterSessionPodHealth{Status: "unknown", Error: err.Error()}
	}
	out := clusterSessionPodHealth{Total: len(pods.Items)}
	for _, pod := range pods.Items {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			out.Running++
		case corev1.PodPending:
			out.Pending++
		case corev1.PodSucceeded:
			out.Succeeded++
		case corev1.PodFailed:
			out.Failed++
		default:
			out.Unknown++
		}
		if podReady(pod) {
			out.Ready++
		} else {
			out.NotReady++
		}
		for _, status := range pod.Status.ContainerStatuses {
			out.Restarts += status.RestartCount
		}
	}
	switch {
	case out.Failed > 0:
		out.Status = "critical"
	case out.Pending > 0 || out.NotReady > 0 || out.Unknown > 0:
		out.Status = "warning"
	default:
		out.Status = "healthy"
	}
	return out
}

func (s *appServer) collectUpgradeHealth(ctx context.Context, now time.Time) clusterUpgradeHealth {
	window := currentUpgradeWindow(now)
	out := clusterUpgradeHealth{
		Status:  "healthy",
		State:   "idle",
		Summary: "No AKS upgrade signals",
		Window:  window,
	}
	if window.Active {
		out.Status = "warning"
		out.State = "window_open"
		out.Summary = "AKS upgrade window is open"
	}
	if s.k8s == nil {
		out.Status = maxHealthStatus(out.Status, "unknown")
		out.State = "unknown"
		out.Summary = "AKS upgrade status unavailable"
		out.Signals = append(out.Signals, "kubernetes client not configured")
		return out
	}
	nodes, err := s.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		out.Status = maxHealthStatus(out.Status, "unknown")
		out.State = "unknown"
		out.Summary = "AKS upgrade status unavailable"
		out.Signals = append(out.Signals, err.Error())
		return out
	}

	imageVersions := map[string]int{}
	kubeletVersions := map[string]int{}
	cordoned := 0
	notReady := 0
	for _, node := range nodes.Items {
		if version := strings.TrimSpace(node.Labels["kubernetes.azure.com/node-image-version"]); version != "" {
			imageVersions[version]++
		}
		if version := strings.TrimSpace(node.Status.NodeInfo.KubeletVersion); version != "" {
			kubeletVersions[version]++
		}
		if node.Spec.Unschedulable {
			cordoned++
		}
		if !nodeConditionTrue(node, corev1.NodeReady) {
			notReady++
		}
	}
	out.NodeImageVersions = versionCounts(imageVersions)
	out.KubeletVersions = versionCounts(kubeletVersions)
	if len(imageVersions) > 1 {
		out.Signals = append(out.Signals, "AKS node image versions are mixed")
	}
	if len(kubeletVersions) > 1 {
		out.Signals = append(out.Signals, "Kubernetes kubelet versions are mixed")
	}
	if cordoned > 0 {
		out.Signals = append(out.Signals, fmt.Sprintf("%d node%s cordoned for scheduling", cordoned, plural(cordoned)))
	}
	if notReady > 0 {
		out.Signals = append(out.Signals, fmt.Sprintf("%d node%s not ready", notReady, plural(notReady)))
	}
	if len(out.Signals) > 0 {
		out.Status = "warning"
		out.State = "active"
		if window.Active && window.SecondsRemaining > 0 {
			out.Summary = fmt.Sprintf("AKS upgrade signals active; %s left in the configured window", shortDuration(time.Duration(window.SecondsRemaining)*time.Second))
		} else {
			out.Summary = "AKS upgrade signals active outside the configured window"
		}
	}
	return out
}

func collectNATSHealth(ctx context.Context, monitorURLs []string, streamName string, expectedStreamReplicas int) clusterNATSHealth {
	out := clusterNATSHealth{
		Status:                "healthy",
		ConfiguredMonitorURLs: len(monitorURLs),
		ExpectedServers:       len(monitorURLs),
		JetStream: clusterJetStream{
			StreamName:             streamName,
			ExpectedStreamReplicas: expectedStreamReplicas,
		},
	}
	if len(monitorURLs) == 0 {
		out.Status = "unknown"
		out.Error = "NATS monitor URLs not configured"
		return out
	}

	client := &http.Client{Timeout: 900 * time.Millisecond}
	type reachableMonitor struct {
		url        string
		serverName string
	}
	var reachable []reachableMonitor
	for _, monitorURL := range monitorURLs {
		var varz natsVarzResponse
		requestCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
		err := fetchNATSJSON(requestCtx, client, monitorURL, "/varz", &varz)
		cancel()
		if err != nil {
			out.Servers = append(out.Servers, clusterNATSServer{
				Reachable: false,
				Error:     err.Error(),
			})
			continue
		}

		out.ReachableServers++
		reachable = append(reachable, reachableMonitor{url: monitorURL, serverName: varz.ServerName})
		out.Servers = append(out.Servers, clusterNATSServer{
			Name:      varz.ServerName,
			Reachable: true,
		})
		mergeNATSVarz(&out.JetStream, varz)
	}

	if len(reachable) != 0 {
		detailAvailable := false
		for _, monitor := range reachable {
			var jsz natsJSZResponse
			requestCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
			err := fetchNATSJSON(requestCtx, client, monitor.url, "/jsz?streams=true&consumers=false&config=true", &jsz)
			cancel()
			if err != nil {
				continue
			}
			detailAvailable = true
			mergeNATSJSZ(&out.JetStream, jsz, streamName, monitor.serverName)
			if out.JetStream.ExpectedStreamReplicas > 0 &&
				out.JetStream.streamReplicaLeaderView &&
				out.JetStream.StreamReplicas >= out.JetStream.ExpectedStreamReplicas &&
				out.JetStream.StreamCurrentReplicas >= out.JetStream.ExpectedStreamReplicas {
				break
			}
		}
		if !detailAvailable {
			out.Warnings = append(out.Warnings, "Live delivery detail unavailable")
		}
	}

	out.Status = classifyNATSHealth(&out)
	if out.Status != "healthy" && len(out.Warnings) == 0 && out.Error == "" {
		out.Warnings = append(out.Warnings, "NATS health degraded")
	}
	return out
}

func currentUpgradeWindow(now time.Time) clusterUpgradeWindow {
	label := envDefault("AKS_UPGRADE_WINDOW_LABEL", "AKS auto-upgrade")
	dayName := envDefault("AKS_UPGRADE_WINDOW_DAY_OF_WEEK", "Sunday")
	startTime := envDefault("AKS_UPGRADE_WINDOW_START_TIME", "06:00")
	utcOffset := envDefault("AKS_UPGRADE_WINDOW_UTC_OFFSET", "+00:00")
	durationHours := envFloatDefault("AKS_UPGRADE_WINDOW_DURATION_HOURS", 12)
	out := clusterUpgradeWindow{
		Configured:    envBoolDefault("AKS_UPGRADE_WINDOW_CONFIGURED", true),
		Label:         label,
		DayOfWeek:     dayName,
		StartTime:     startTime,
		UTCOffset:     utcOffset,
		DurationHours: durationHours,
	}
	if !out.Configured {
		return out
	}
	weekday, ok := parseWeekday(dayName)
	if !ok {
		out.WindowComputationFailed = fmt.Sprintf("invalid day of week %q", dayName)
		return out
	}
	startHour, startMinute, ok := parseHHMM(startTime)
	if !ok {
		out.WindowComputationFailed = fmt.Sprintf("invalid start time %q", startTime)
		return out
	}
	offset, ok := parseUTCOffset(utcOffset)
	if !ok {
		out.WindowComputationFailed = fmt.Sprintf("invalid UTC offset %q", utcOffset)
		return out
	}
	if durationHours <= 0 {
		out.WindowComputationFailed = fmt.Sprintf("invalid duration %.2f", durationHours)
		return out
	}
	location := time.FixedZone(utcOffset, int(offset.Seconds()))
	localNow := now.In(location)
	daysSince := (int(localNow.Weekday()) - int(weekday) + 7) % 7
	startLocal := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), startHour, startMinute, 0, 0, location).AddDate(0, 0, -daysSince)
	if startLocal.After(localNow) {
		startLocal = startLocal.AddDate(0, 0, -7)
	}
	duration := time.Duration(durationHours * float64(time.Hour))
	endLocal := startLocal.Add(duration)
	if !localNow.Before(endLocal) {
		startLocal = startLocal.AddDate(0, 0, 7)
		endLocal = startLocal.Add(duration)
	}
	startUTC := startLocal.UTC()
	endUTC := endLocal.UTC()
	if !now.Before(startUTC) && now.Before(endUTC) {
		out.Active = true
		out.CurrentWindowStartedAt = startUTC.Format(time.RFC3339)
		out.CurrentWindowEndsAt = endUTC.Format(time.RFC3339)
		out.SecondsRemaining = int64(endUTC.Sub(now).Seconds())
		return out
	}
	out.NextWindowStartsAt = startUTC.Format(time.RFC3339)
	out.SecondsUntilNextWindow = int64(startUTC.Sub(now).Seconds())
	return out
}

type natsVarzResponse struct {
	ServerName    string `json:"server_name"`
	SlowConsumers int64  `json:"slow_consumers"`
	JetStream     struct {
		Config struct {
			MaxMemory int64 `json:"max_memory"`
		} `json:"config"`
		Stats struct {
			Memory         int64 `json:"memory"`
			ReservedMemory int64 `json:"reserved_memory"`
		} `json:"stats"`
		Meta struct {
			Pending int64 `json:"pending"`
		} `json:"meta"`
	} `json:"jetstream"`
}

type natsJSZStreamReplica struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

type natsJSZResponse struct {
	Memory         int64 `json:"memory"`
	ReservedMemory int64 `json:"reserved_memory"`
	Streams        int64 `json:"streams"`
	Consumers      int64 `json:"consumers"`
	Messages       int64 `json:"messages"`
	Bytes          int64 `json:"bytes"`
	Config         struct {
		MaxMemory int64 `json:"max_memory"`
	} `json:"config"`
	MetaCluster struct {
		Pending int64 `json:"pending"`
	} `json:"meta_cluster"`
	AccountDetails []struct {
		StreamDetail []struct {
			Name    string `json:"name"`
			Cluster struct {
				Leader   string                 `json:"leader"`
				Replicas []natsJSZStreamReplica `json:"replicas"`
			} `json:"cluster"`
			Config struct {
				NumReplicas int `json:"num_replicas"`
			} `json:"config"`
			State struct {
				Messages      int64 `json:"messages"`
				Bytes         int64 `json:"bytes"`
				ConsumerCount int64 `json:"consumer_count"`
			} `json:"state"`
		} `json:"stream_detail"`
	} `json:"account_details"`
}

func mergeNATSVarz(out *clusterJetStream, varz natsVarzResponse) {
	out.MemoryBytes = maxInt64(out.MemoryBytes, varz.JetStream.Stats.Memory)
	out.MaxMemoryBytes = maxInt64(out.MaxMemoryBytes, varz.JetStream.Config.MaxMemory)
	out.ReservedMemoryBytes = maxInt64(out.ReservedMemoryBytes, varz.JetStream.Stats.ReservedMemory)
	out.MetaPending = maxInt64(out.MetaPending, varz.JetStream.Meta.Pending)
	out.SlowConsumers = maxInt64(out.SlowConsumers, varz.SlowConsumers)
	updateNATSMemoryUtilization(out)
}

func mergeNATSJSZ(out *clusterJetStream, jsz natsJSZResponse, streamName, localServerName string) {
	out.MemoryBytes = maxInt64(out.MemoryBytes, jsz.Memory)
	out.MaxMemoryBytes = maxInt64(out.MaxMemoryBytes, jsz.Config.MaxMemory)
	out.ReservedMemoryBytes = maxInt64(out.ReservedMemoryBytes, jsz.ReservedMemory)
	out.MetaPending = maxInt64(out.MetaPending, jsz.MetaCluster.Pending)
	out.Streams = maxInt64(out.Streams, jsz.Streams)
	out.Consumers = maxInt64(out.Consumers, jsz.Consumers)
	out.Messages = maxInt64(out.Messages, jsz.Messages)
	out.Bytes = maxInt64(out.Bytes, jsz.Bytes)
	for _, account := range jsz.AccountDetails {
		for _, stream := range account.StreamDetail {
			if streamName != "" && stream.Name != streamName {
				continue
			}
			out.StreamName = stream.Name
			currentReplicas := streamCurrentReplicaCount(stream.Cluster.Replicas, localServerName)
			configuredReplicas := stream.Config.NumReplicas
			if configuredReplicas <= 0 {
				configuredReplicas = maxInt(currentReplicas, len(stream.Cluster.Replicas))
			}
			isLeaderView := strings.TrimSpace(stream.Cluster.Leader) != "" && strings.TrimSpace(stream.Cluster.Leader) == strings.TrimSpace(localServerName)
			if shouldUseNATSReplicaView(out, configuredReplicas, currentReplicas, isLeaderView) {
				out.StreamReplicas = configuredReplicas
				out.StreamCurrentReplicas = currentReplicas
				out.streamReplicaLeaderView = isLeaderView
				updateNATSReplicaLag(out)
			}
			out.StreamMessages = stream.State.Messages
			out.StreamBytes = stream.State.Bytes
			out.StreamConsumers = stream.State.ConsumerCount
			updateNATSMemoryUtilization(out)
			return
		}
	}
	updateNATSMemoryUtilization(out)
}

func classifyNATSHealth(out *clusterNATSHealth) string {
	switch {
	case out == nil:
		return "unknown"
	case out.ConfiguredMonitorURLs == 0:
		return "unknown"
	case out.ReachableServers == 0:
		out.Error = "no NATS monitor endpoints reachable"
		return "critical"
	}

	status := "healthy"
	addWarning := func(message string) {
		status = maxHealthStatus(status, "warning")
		out.Warnings = append(out.Warnings, message)
	}
	addCritical := func(message string) {
		status = "critical"
		out.Warnings = append(out.Warnings, message)
	}

	if out.ReachableServers < out.ExpectedServers {
		addWarning(fmt.Sprintf("Live delivery monitors %d/%d reachable", out.ReachableServers, out.ExpectedServers))
	}
	if out.JetStream.MemoryUtilization >= 0.90 {
		addCritical("Live delivery memory over 90%")
	} else if out.JetStream.MemoryUtilization >= 0.75 {
		addWarning("Live delivery memory over 75%")
	}
	if out.JetStream.MetaPending > 50 {
		addWarning("Live delivery metadata backlog over 50")
	}
	if out.JetStream.SlowConsumers > 0 {
		addWarning("Live delivery has slow consumers")
	}
	if out.JetStream.ExpectedStreamReplicas > 0 {
		switch {
		case out.JetStream.StreamReplicas == 0:
			addWarning("Live delivery replica detail unavailable")
		case out.JetStream.StreamReplicas < out.JetStream.ExpectedStreamReplicas:
			addWarning(fmt.Sprintf("Live delivery configured replicas %d/%d", out.JetStream.StreamReplicas, out.JetStream.ExpectedStreamReplicas))
		case out.JetStream.StreamCurrentReplicas > 0 && out.JetStream.StreamCurrentReplicas < out.JetStream.ExpectedStreamReplicas:
			addWarning(fmt.Sprintf("Live delivery replicas %d/%d current", out.JetStream.StreamCurrentReplicas, out.JetStream.ExpectedStreamReplicas))
		}
	}
	return status
}

func fetchNATSJSON(ctx context.Context, client *http.Client, baseURL, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func natsMonitorURLs() []string {
	raw := strings.TrimSpace(os.Getenv("NATS_MONITOR_URLS"))
	if raw == "" {
		raw = "http://tank-nats-0.tank-nats-headless.nats.svc.cluster.local:8222,http://tank-nats-1.tank-nats-headless.nats.svc.cluster.local:8222,http://tank-nats-2.tank-nats-headless.nats.svc.cluster.local:8222"
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	var urls []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	return urls
}

func expectedNATSStreamReplicas() int {
	raw := strings.TrimSpace(os.Getenv("NATS_STREAM_REPLICAS"))
	if raw == "" {
		return 3
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return 3
	}
	return parsed
}

func envFloatDefault(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseWeekday(raw string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sunday":
		return time.Sunday, true
	case "monday":
		return time.Monday, true
	case "tuesday":
		return time.Tuesday, true
	case "wednesday":
		return time.Wednesday, true
	case "thursday":
		return time.Thursday, true
	case "friday":
		return time.Friday, true
	case "saturday":
		return time.Saturday, true
	default:
		return time.Sunday, false
	}
}

func parseHHMM(raw string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMinute := strconv.Atoi(parts[1])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

func parseUTCOffset(raw string) (time.Duration, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "Z" || trimmed == "UTC" || trimmed == "+00:00" || trimmed == "-00:00" {
		return 0, true
	}
	if len(trimmed) != 6 || (trimmed[0] != '+' && trimmed[0] != '-') || trimmed[3] != ':' {
		return 0, false
	}
	hour, errHour := strconv.Atoi(trimmed[1:3])
	minute, errMinute := strconv.Atoi(trimmed[4:6])
	if errHour != nil || errMinute != nil || hour > 23 || minute > 59 {
		return 0, false
	}
	offset := time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute
	if trimmed[0] == '-' {
		offset = -offset
	}
	return offset, true
}

func nodeConditionTrue(node corev1.Node, conditionType corev1.NodeConditionType) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podReady(pod corev1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.ContainersReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func worstHealthStatus(statuses ...string) string {
	out := "healthy"
	for _, status := range statuses {
		out = maxHealthStatus(out, status)
	}
	return out
}

func maxHealthStatus(left, right string) string {
	if healthRank(right) > healthRank(left) {
		return right
	}
	return left
}

func healthRank(status string) int {
	switch status {
	case "critical":
		return 3
	case "warning":
		return 2
	case "unknown":
		return 1
	default:
		return 0
	}
}

func maxInt64(left, right int64) int64 {
	if right > left {
		return right
	}
	return left
}

func maxInt(left, right int) int {
	if right > left {
		return right
	}
	return left
}

func versionCounts(counts map[string]int) []clusterUpgradeVersionCount {
	out := make([]clusterUpgradeVersionCount, 0, len(counts))
	for version, count := range counts {
		out = append(out, clusterUpgradeVersionCount{Version: version, Count: count})
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Version < out[i].Version {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func shortDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0m"
	}
	minutes := int64((duration + time.Minute - 1) / time.Minute)
	hours := minutes / 60
	mins := minutes % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

func streamCurrentReplicaCount(replicas []natsJSZStreamReplica, localServerName string) int {
	current := map[string]struct{}{}
	if localServerName = strings.TrimSpace(localServerName); localServerName != "" {
		current[localServerName] = struct{}{}
	}
	for _, replica := range replicas {
		if replica.Current && strings.TrimSpace(replica.Name) != "" {
			current[replica.Name] = struct{}{}
		}
	}
	return len(current)
}

func shouldUseNATSReplicaView(out *clusterJetStream, configuredReplicas, currentReplicas int, isLeaderView bool) bool {
	if isLeaderView {
		return true
	}
	if out.streamReplicaLeaderView {
		return false
	}
	if configuredReplicas > out.StreamReplicas {
		return true
	}
	if configuredReplicas == out.StreamReplicas && currentReplicas > out.StreamCurrentReplicas {
		return true
	}
	return false
}

func updateNATSReplicaLag(out *clusterJetStream) {
	if out.StreamReplicas > out.StreamCurrentReplicas {
		out.StreamLaggingReplicas = out.StreamReplicas - out.StreamCurrentReplicas
		return
	}
	out.StreamLaggingReplicas = 0
}

func updateNATSMemoryUtilization(out *clusterJetStream) {
	if out.MaxMemoryBytes > 0 {
		out.MemoryUtilization = float64(out.MemoryBytes) / float64(out.MaxMemoryBytes)
	}
}
