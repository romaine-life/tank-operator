package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHandleClusterHealthSummarizesNodesSessionsAndNATS(t *testing.T) {
	nats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/varz":
			writeJSON(w, http.StatusOK, map[string]any{
				"server_name":    "tank-nats-0",
				"slow_consumers": 0,
				"jetstream": map[string]any{
					"config": map[string]any{"max_memory": 1000},
					"stats":  map[string]any{"memory": 500, "reserved_memory": 500},
					"meta":   map[string]any{"pending": 0},
				},
			})
		case "/jsz":
			writeJSON(w, http.StatusOK, map[string]any{
				"memory":    500,
				"streams":   1,
				"consumers": 2,
				"messages":  20,
				"bytes":     500,
				"config":    map[string]any{"max_memory": 1000},
				"meta_cluster": map[string]any{
					"pending": 0,
				},
				"account_details": []map[string]any{
					{
						"stream_detail": []map[string]any{
							{
								"name": "TANK_SESSION_BUS",
								"cluster": map[string]any{
									"leader": "tank-nats-0",
									"replicas": []map[string]any{
										{"name": "tank-nats-1", "current": true},
										{"name": "tank-nats-2", "current": true},
									},
								},
								"config": map[string]any{"num_replicas": 3},
								"state": map[string]any{
									"messages":       20,
									"bytes":          500,
									"consumer_count": 2,
								},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer nats.Close()
	t.Setenv("NATS_MONITOR_URLS", nats.URL)
	t.Setenv("NATS_STREAM_REPLICAS", "3")
	t.Setenv("NATS_STREAM", "TANK_SESSION_BUS")

	app := &appServer{
		verifier:  authVerifierForTests(t),
		k8s:       fake.NewSimpleClientset(healthyNode("node-a"), memoryPressureNode("node-b"), readySessionPod("session-1"), pendingSessionPod("session-2")),
		namespace: "tank-operator-sessions",
	}
	req := httptest.NewRequest(http.MethodGet, "/api/cluster-health", nil)
	req.Header.Set("Authorization", "Bearer "+signedMainToken(t, "secret", "user@example.com"))
	rec := httptest.NewRecorder()

	app.handleClusterHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body clusterHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "warning" {
		t.Fatalf("status = %q, want warning: %#v", body.Status, body)
	}
	if body.Nodes.Total != 2 || body.Nodes.Ready != 2 || body.Nodes.MemoryPressureNodes != 1 {
		t.Fatalf("nodes = %#v", body.Nodes)
	}
	if body.Sessions.Total != 2 || body.Sessions.Ready != 1 || body.Sessions.Pending != 1 {
		t.Fatalf("sessions = %#v", body.Sessions)
	}
	if body.NATS.ReachableServers != 1 || body.NATS.JetStream.MemoryUtilization != 0.5 {
		t.Fatalf("nats = %#v", body.NATS)
	}
	if body.NATS.Status != "healthy" || len(body.NATS.Warnings) != 0 {
		t.Fatalf("nats should be healthy, got %#v", body.NATS)
	}
	if body.NATS.JetStream.StreamReplicas != 3 || body.NATS.JetStream.ExpectedStreamReplicas != 3 || body.NATS.JetStream.StreamCurrentReplicas != 3 {
		t.Fatalf("nats stream replicas = %#v", body.NATS.JetStream)
	}
	if body.Upgrade.MaintenanceWindow.DurationHours != 12 || body.Upgrade.AutoUpgradeChannel != "patch" {
		t.Fatalf("upgrade = %#v", body.Upgrade)
	}
	if !strings.Contains(clusterMessagesForTest(body.Messages), "1 node under memory pressure") ||
		!strings.Contains(clusterMessagesForTest(body.Messages), "1 session pod pending") {
		t.Fatalf("messages = %#v", body.Messages)
	}
}

func TestCollectNATSHealthCriticalWhenMonitorUnreachable(t *testing.T) {
	nats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	url := nats.URL
	nats.Close()

	got := collectNATSHealth(context.Background(), []string{url}, "TANK_SESSION_BUS", 3)
	if got.Status != "critical" {
		t.Fatalf("status = %q, want critical: %#v", got.Status, got)
	}
	if got.ReachableServers != 0 || got.Error == "" {
		t.Fatalf("nats = %#v", got)
	}
}

func TestCollectNATSHealthWarnsWhenStreamReplicasAreLagging(t *testing.T) {
	nats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/varz":
			writeJSON(w, http.StatusOK, map[string]any{
				"server_name":    "tank-nats-0",
				"slow_consumers": 0,
				"jetstream": map[string]any{
					"config": map[string]any{"max_memory": 1000},
					"stats":  map[string]any{"memory": 500, "reserved_memory": 500},
					"meta":   map[string]any{"pending": 0},
				},
			})
		case "/jsz":
			writeJSON(w, http.StatusOK, map[string]any{
				"memory":  500,
				"streams": 1,
				"config":  map[string]any{"max_memory": 1000},
				"account_details": []map[string]any{
					{
						"stream_detail": []map[string]any{
							{
								"name": "TANK_SESSION_BUS",
								"cluster": map[string]any{
									"leader": "tank-nats-0",
									"replicas": []map[string]any{
										{"name": "tank-nats-1", "current": true},
										{"name": "tank-nats-2", "current": false},
									},
								},
								"config": map[string]any{"num_replicas": 3},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer nats.Close()

	got := collectNATSHealth(context.Background(), []string{nats.URL}, "TANK_SESSION_BUS", 3)
	if got.Status != "warning" {
		t.Fatalf("status = %q, want warning: %#v", got.Status, got)
	}
	if got.JetStream.StreamReplicas != 3 || got.JetStream.StreamCurrentReplicas != 2 || got.JetStream.StreamLaggingReplicas != 1 {
		t.Fatalf("jetstream = %#v", got.JetStream)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "Live delivery replicas 2/3 current") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
}

func TestCollectNATSHealthWarnsWhenConsumersAreBacklogged(t *testing.T) {
	nats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/varz":
			writeJSON(w, http.StatusOK, map[string]any{
				"server_name":    "tank-nats-0",
				"slow_consumers": 0,
				"jetstream": map[string]any{
					"config": map[string]any{"max_memory": 1000},
					"stats":  map[string]any{"memory": 500, "reserved_memory": 500},
					"meta":   map[string]any{"pending": 0},
				},
			})
		case "/jsz":
			if r.URL.Query().Get("consumers") != "true" {
				t.Fatalf("jsz consumers query = %q, want true", r.URL.RawQuery)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"memory":  500,
				"streams": 1,
				"config":  map[string]any{"max_memory": 1000},
				"account_details": []map[string]any{
					{
						"stream_detail": []map[string]any{
							{
								"name": "TANK_SESSION_BUS",
								"cluster": map[string]any{
									"leader":   "tank-nats-0",
									"replicas": []map[string]any{},
								},
								"config": map[string]any{"num_replicas": 1},
								"state": map[string]any{
									"messages":       200,
									"bytes":          5000,
									"consumer_count": 3,
								},
								"consumer_detail": []map[string]any{
									{
										"name":            "claude_default_1",
										"num_pending":     12,
										"num_ack_pending": 2,
										"num_redelivered": 1,
										"num_waiting":     0,
										"delivered":       map[string]any{"stream_seq": 20},
										"ack_floor":       map[string]any{"stream_seq": 8},
									},
									{"name": "codex_default_2", "num_pending": 4},
								},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer nats.Close()

	got := collectNATSHealth(context.Background(), []string{nats.URL}, "TANK_SESSION_BUS", 1)
	if got.Status != "warning" {
		t.Fatalf("status = %q, want warning: %#v", got.Status, got)
	}
	if got.JetStream.ConsumerPending != 16 || got.JetStream.ConsumerAckPending != 2 || got.JetStream.ConsumerRedelivered != 1 {
		t.Fatalf("consumer backlog = %#v", got.JetStream)
	}
	if got.JetStream.BackloggedConsumers != 2 || len(got.JetStream.TopConsumerBacklogs) != 2 {
		t.Fatalf("top consumer backlog = %#v", got.JetStream)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "consumer backlog 16 pending across 2 consumers") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
}

func TestCollectNATSHealthPrefersStreamLeaderReplicaView(t *testing.T) {
	follower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/varz":
			writeJSON(w, http.StatusOK, map[string]any{
				"server_name":    "tank-nats-0",
				"slow_consumers": 0,
				"jetstream": map[string]any{
					"config": map[string]any{"max_memory": 1000},
					"stats":  map[string]any{"memory": 500, "reserved_memory": 500},
					"meta":   map[string]any{"pending": 0},
				},
			})
		case "/jsz":
			writeJSON(w, http.StatusOK, map[string]any{
				"memory":  500,
				"streams": 1,
				"config":  map[string]any{"max_memory": 1000},
				"account_details": []map[string]any{
					{
						"stream_detail": []map[string]any{
							{
								"name": "TANK_SESSION_BUS",
								"cluster": map[string]any{
									"leader": "tank-nats-1",
									"replicas": []map[string]any{
										{"name": "tank-nats-1", "current": true},
										{"name": "tank-nats-2", "current": false},
									},
								},
								"config": map[string]any{"num_replicas": 3},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer follower.Close()

	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/varz":
			writeJSON(w, http.StatusOK, map[string]any{
				"server_name":    "tank-nats-1",
				"slow_consumers": 0,
				"jetstream": map[string]any{
					"config": map[string]any{"max_memory": 1000},
					"stats":  map[string]any{"memory": 500, "reserved_memory": 500},
					"meta":   map[string]any{"pending": 0},
				},
			})
		case "/jsz":
			writeJSON(w, http.StatusOK, map[string]any{
				"memory":  500,
				"streams": 1,
				"config":  map[string]any{"max_memory": 1000},
				"account_details": []map[string]any{
					{
						"stream_detail": []map[string]any{
							{
								"name": "TANK_SESSION_BUS",
								"cluster": map[string]any{
									"leader": "tank-nats-1",
									"replicas": []map[string]any{
										{"name": "tank-nats-0", "current": true},
										{"name": "tank-nats-2", "current": true},
									},
								},
								"config": map[string]any{"num_replicas": 3},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer leader.Close()

	got := collectNATSHealth(context.Background(), []string{follower.URL, leader.URL}, "TANK_SESSION_BUS", 3)
	if got.Status != "healthy" {
		t.Fatalf("status = %q, want healthy: %#v", got.Status, got)
	}
	if got.JetStream.StreamReplicas != 3 || got.JetStream.StreamCurrentReplicas != 3 || got.JetStream.StreamLaggingReplicas != 0 {
		t.Fatalf("jetstream = %#v", got.JetStream)
	}
	if strings.Contains(strings.Join(got.Warnings, "\n"), "Live delivery replicas") {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
}

func TestCollectUpgradeHealthDetectsMixedNodeVersionsAndMaintenanceWindow(t *testing.T) {
	t.Setenv("AKS_MAINTENANCE_WINDOW_DAY_OF_WEEK", "Sunday")
	t.Setenv("AKS_MAINTENANCE_WINDOW_START_TIME", "06:00")
	t.Setenv("AKS_MAINTENANCE_WINDOW_UTC_OFFSET", "+00:00")
	t.Setenv("AKS_MAINTENANCE_WINDOW_DURATION_HOURS", "12")
	app := &appServer{k8s: fake.NewSimpleClientset(
		aksNode("node-a", "v1.34.7", "AKSUbuntu-202605.01", false),
		aksNode("node-b", "v1.34.8", "AKSUbuntu-202605.14", true),
	)}
	now := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)

	got := app.collectUpgradeHealth(context.Background(), now)
	if got.Status != "warning" || !got.Detected {
		t.Fatalf("upgrade = %#v", got)
	}
	if !got.MaintenanceWindow.Active || got.MaintenanceWindow.SecondsRemaining != int64(8*time.Hour/time.Second) {
		t.Fatalf("window = %#v", got.MaintenanceWindow)
	}
	if len(got.KubeletVersions) != 2 || len(got.NodeImageVersions) != 2 || len(got.UnschedulableNodes) != 1 {
		t.Fatalf("upgrade details = %#v", got)
	}
	if !strings.Contains(strings.Join(got.Signals, "\n"), "mixed kubelet versions") {
		t.Fatalf("signals = %#v", got.Signals)
	}
}

func healthyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
}

func aksNode(name, kubeletVersion, nodeImageVersion string, unschedulable bool) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kubernetes.azure.com/node-image-version": nodeImageVersion,
			},
		},
		Spec: corev1.NodeSpec{Unschedulable: unschedulable},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: kubeletVersion},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func clusterMessagesForTest(messages []clusterHealthMessage) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Message)
	}
	return strings.Join(parts, "\n")
}

func memoryPressureNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
		}},
	}
}

func readySessionPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tank-operator-sessions"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sandbox", Ready: true, RestartCount: 1},
			},
		},
	}
}

func pendingSessionPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tank-operator-sessions"},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
}
