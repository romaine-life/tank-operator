// Package sessionstream tracks the live state of every open
// /api/sessions/{id}/events SSE handler so admin operators can curl a
// diagnostic surface that explains why a specific browser stopped
// receiving live transcript events without having to read browser
// devtools. Per memory/feedback_no_devtools_build_surfaces_instead.md
// the user-trust constraint on this repo is that observability lives
// behind curl-able endpoints, slog lines, and Prometheus — never the
// browser network panel.
//
// The registry is in-memory and per-replica. The admin endpoint reads
// one replica at a time; that matches the operational shape (we run
// one orchestrator replica at a time today, and SSE handlers are bound
// to a specific replica's TCP socket regardless).
//
// What the registry answers, paired with the new Prometheus counters
// in observability.go:
//   - "Is this open stream receiving wakes?" — inspect LastWakeAt,
//     LastWakeSubject, and WakesReceived for the stream whose storage_key
//     matches the persister's slog line.
//   - "Did the SSE handler emit events post-wake?" — compare LastEmitAt
//     and EmitsTotal against the durable ledger's row count.
//   - "Did durable rows only arrive through heartbeat polling?" — check
//     tank_session_event_stream_heartbeat_catchup_total and the matching
//     "session event stream caught up from heartbeat" slog line.
//   - "Is the connection zombie?" — LastWakeAt fresh, LastEmitAt fresh,
//     but the browser claims no events arrived. That points the diagnosis
//     at the client / proxy layer instead of the backend wake fabric.
package sessionstream

import (
	"sync"
	"time"
)

// StreamState is the per-open-SSE-handler diagnostic state. One
// instance is owned by exactly one SSE handler goroutine, but the
// admin endpoint reads a snapshot under the embedded mutex.
//
// Field names match what the admin endpoint emits as JSON; if you
// add a field, also surface it in Snapshot() so the diagnostic
// stays complete.
type StreamState struct {
	streamID   string
	sessionID  string
	storageKey string
	email      string
	openedAt   time.Time

	mu                  sync.Mutex
	lastWakeAt          time.Time
	lastWakeSubject     string
	lastPageReadAt      time.Time
	lastPageEmitCount   int
	lastEmitAt          time.Time
	lastEmitOrderKey    string
	lastEmitEventType   string
	cursorAfterOrderKey string
	lastHeartbeatAt     time.Time

	wakesReceived     int64
	pagesReadEmpty    int64
	pagesReadNonEmpty int64
	emitsTotal        int64
	heartbeatsSent    int64
}

// NewStreamState constructs the state for one freshly opened SSE handler.
// The constructor is the only place streamID / sessionID / storageKey /
// email / openedAt are set — they're identity, not mutable state.
func NewStreamState(streamID, sessionID, storageKey, email string, openedAt time.Time, initialCursor string) *StreamState {
	return &StreamState{
		streamID:            streamID,
		sessionID:           sessionID,
		storageKey:          storageKey,
		email:               email,
		openedAt:            openedAt,
		cursorAfterOrderKey: initialCursor,
	}
}

// RecordWake is called from the NATS subscribe callback whenever a
// wake fires for this stream's storage key. The subject is recorded
// alongside the timestamp so the admin endpoint can show exactly what
// subject reached this open stream.
func (s *StreamState) RecordWake(now time.Time, subject string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastWakeAt = now
	s.lastWakeSubject = subject
	s.wakesReceived++
}

// RecordPageRead is called after each writeSessionEventStreamPage
// completes, whether or not it emitted any events. Distinguishing
// empty reads from non-empty ones lets the admin endpoint show
// "we keep getting woken but the read finds nothing" — the candidate-B
// signature for read-replica visibility lag or a cursor that has
// jumped past pending rows.
func (s *StreamState) RecordPageRead(now time.Time, emitCount int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPageReadAt = now
	s.lastPageEmitCount = emitCount
	if emitCount == 0 {
		s.pagesReadEmpty++
	} else {
		s.pagesReadNonEmpty++
	}
}

// RecordEmit is called for each projected-row SSE event written to the socket.
// The cursor advance is recorded so the admin endpoint can compare
// against the durable ledger's tip.
func (s *StreamState) RecordEmit(now time.Time, orderKey, eventType, cursorAfter string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEmitAt = now
	s.lastEmitOrderKey = orderKey
	s.lastEmitEventType = eventType
	if cursorAfter != "" {
		s.cursorAfterOrderKey = cursorAfter
	}
	s.emitsTotal++
}

// RecordHeartbeat is called on each keep-alive frame the handler
// writes during idle. A stream with rising HeartbeatsSent but flat
// EmitsTotal is the canonical "SSE is alive, no events flowing"
// shape — useful even before we add the matching client-side
// receipt counter.
func (s *StreamState) RecordHeartbeat(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHeartbeatAt = now
	s.heartbeatsSent++
}

// Snapshot is the wire shape the admin endpoint returns. RFC3339Nano
// timestamps are zero-valued (empty string) until the first event of
// each kind fires, which lets the JSON consumer distinguish "never"
// from "long ago."
type Snapshot struct {
	StreamID            string  `json:"stream_id"`
	SessionID           string  `json:"session_id"`
	StorageKey          string  `json:"storage_key"`
	Email               string  `json:"email"`
	OpenedAt            string  `json:"opened_at"`
	OpenSeconds         float64 `json:"open_seconds"`
	LastWakeAt          string  `json:"last_wake_at,omitempty"`
	LastWakeSubject     string  `json:"last_wake_subject,omitempty"`
	LastPageReadAt      string  `json:"last_page_read_at,omitempty"`
	LastPageEmitCount   int     `json:"last_page_emit_count"`
	LastEmitAt          string  `json:"last_emit_at,omitempty"`
	LastEmitOrderKey    string  `json:"last_emit_order_key,omitempty"`
	LastEmitEventType   string  `json:"last_emit_event_type,omitempty"`
	CursorAfterOrderKey string  `json:"cursor_after_order_key,omitempty"`
	LastHeartbeatAt     string  `json:"last_heartbeat_at,omitempty"`

	WakesReceived     int64 `json:"wakes_received"`
	PagesReadEmpty    int64 `json:"pages_read_empty"`
	PagesReadNonEmpty int64 `json:"pages_read_non_empty"`
	EmitsTotal        int64 `json:"emits_total"`
	HeartbeatsSent    int64 `json:"heartbeats_sent"`
}

// Snapshot copies the state under the lock so the admin endpoint can
// JSON-encode without holding the stream's mutex during I/O.
func (s *StreamState) Snapshot(now time.Time) Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		StreamID:            s.streamID,
		SessionID:           s.sessionID,
		StorageKey:          s.storageKey,
		Email:               s.email,
		OpenedAt:            formatTime(s.openedAt),
		OpenSeconds:         now.Sub(s.openedAt).Seconds(),
		LastWakeAt:          formatTime(s.lastWakeAt),
		LastWakeSubject:     s.lastWakeSubject,
		LastPageReadAt:      formatTime(s.lastPageReadAt),
		LastPageEmitCount:   s.lastPageEmitCount,
		LastEmitAt:          formatTime(s.lastEmitAt),
		LastEmitOrderKey:    s.lastEmitOrderKey,
		LastEmitEventType:   s.lastEmitEventType,
		CursorAfterOrderKey: s.cursorAfterOrderKey,
		LastHeartbeatAt:     formatTime(s.lastHeartbeatAt),
		WakesReceived:       s.wakesReceived,
		PagesReadEmpty:      s.pagesReadEmpty,
		PagesReadNonEmpty:   s.pagesReadNonEmpty,
		EmitsTotal:          s.emitsTotal,
		HeartbeatsSent:      s.heartbeatsSent,
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// Registry is the process-wide map of open SSE handlers. Safe for
// concurrent use. One Registry per orchestrator process.
type Registry struct {
	mu      sync.RWMutex
	streams map[string]*StreamState
}

// NewRegistry constructs an empty registry. The orchestrator wires
// exactly one instance into appServer at boot.
func NewRegistry() *Registry {
	return &Registry{streams: make(map[string]*StreamState)}
}

// Register adds a fresh stream's state. The streamID is the registry
// key — generated by the caller (the SSE handler) so the caller can
// hold the pointer for cheap per-event updates and deregister at
// close time without re-keying.
func (r *Registry) Register(state *StreamState) {
	if r == nil || state == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams[state.streamID] = state
}

// Deregister removes a closed stream's state. Safe to call with an
// unknown streamID (no-op) so the SSE handler's defer doesn't need
// to coordinate with Register's race window.
func (r *Registry) Deregister(streamID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.streams, streamID)
}

// Snapshot returns a per-stream snapshot of every currently-open
// stream. Sorted by OpenedAt ascending so admin endpoint output is
// stable across calls (operators scrolling a long list during
// diagnosis don't see rows shuffle on refresh).
func (r *Registry) Snapshot(now time.Time) []Snapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	streams := make([]*StreamState, 0, len(r.streams))
	for _, s := range r.streams {
		streams = append(streams, s)
	}
	r.mu.RUnlock()

	out := make([]Snapshot, 0, len(streams))
	for _, s := range streams {
		out = append(out, s.Snapshot(now))
	}
	sortSnapshotsByOpenedAtAsc(out)
	return out
}

// Len returns the number of currently registered streams. Tests
// assert against this to verify Register/Deregister balance.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.streams)
}

func sortSnapshotsByOpenedAtAsc(snapshots []Snapshot) {
	// Insertion sort: snapshots count is at most "currently open SSE
	// streams" which is bounded by active browsers, well under 100 on
	// this cluster's scale. Avoids pulling sort just for this.
	for i := 1; i < len(snapshots); i++ {
		current := snapshots[i]
		j := i - 1
		for j >= 0 && snapshots[j].OpenedAt > current.OpenedAt {
			snapshots[j+1] = snapshots[j]
			j--
		}
		snapshots[j+1] = current
	}
}
