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
	if body.Upgrade.Window.DurationHours != 12 || body.Upgrade.Window.DayOfWeek != "Sunday" {
		t.Fatalf("upgrade window = %#v", body.Upgrade.Window)
	}
}

func TestCollectUpgradeHealthWarnsDuringWindowAndReportsRemainingTime(t *testing.T) {
	t.Setenv("AKS_UPGRADE_WINDOW_DAY_OF_WEEK", "Sunday")
	t.Setenv("AKS_UPGRADE_WINDOW_START_TIME", "06:00")
	t.Setenv("AKS_UPGRADE_WINDOW_UTC_OFFSET", "+00:00")
	t.Setenv("AKS_UPGRADE_WINDOW_DURATION_HOURS", "12")
	app := &appServer{
		k8s: fake.NewSimpleClientset(aksNode("node-a", "AKSUbuntu-2204gen2containerd-202406.12.0", "v1.31.8", false)),
	}

	got := app.collectUpgradeHealth(context.Background(), time.Date(2026, 6, 14, 7, 0, 0, 0, time.UTC))

	if got.State != "window_open" || got.Status != "warning" {
		t.Fatalf("upgrade state = %#v", got)
	}
	if !got.Window.Active || got.Window.SecondsRemaining != 11*60*60 {
		t.Fatalf("window = %#v", got.Window)
	}
}

func TestCollectUpgradeHealthDetectsMixedNodeVersionsAndCordons(t *testing.T) {
	t.Setenv("AKS_UPGRADE_WINDOW_DAY_OF_WEEK", "Sunday")
	t.Setenv("AKS_UPGRADE_WINDOW_START_TIME", "06:00")
	t.Setenv("AKS_UPGRADE_WINDOW_UTC_OFFSET", "+00:00")
	t.Setenv("AKS_UPGRADE_WINDOW_DURATION_HOURS", "12")
	app := &appServer{
		k8s: fake.NewSimpleClientset(
			aksNode("node-a", "AKSUbuntu-2204gen2containerd-202406.12.0", "v1.31.8", true),
			aksNode("node-b", "AKSUbuntu-2204gen2containerd-202406.20.0", "v1.31.9", false),
		),
	}

	got := app.collectUpgradeHealth(context.Background(), time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC))

	if got.State != "active" || got.Status != "warning" {
		t.Fatalf("upgrade state = %#v", got)
	}
	joined := strings.Join(got.Signals, "\n")
	for _, want := range []string{
		"AKS node image versions are mixed",
		"Kubernetes kubelet versions are mixed",
		"1 node cordoned for scheduling",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("signals = %#v, missing %q", got.Signals, want)
		}
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

func healthyNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
}

func aksNode(name, imageVersion, kubeletVersion string, unschedulable bool) *corev1.Node {
	node := healthyNode(name)
	node.Labels = map[string]string{
		"kubernetes.azure.com/node-image-version": imageVersion,
	}
	node.Spec.Unschedulable = unschedulable
	node.Status.NodeInfo.KubeletVersion = kubeletVersion
	return node
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
