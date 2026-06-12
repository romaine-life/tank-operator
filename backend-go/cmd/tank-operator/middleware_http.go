package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/romaine-life/tank-operator/backend-go/internal/auth"
)

// requestMetaKey is the context key for the per-request mutable metadata
// the middleware threads through every handler. requireAuth populates the
// email field; the middleware reads it after the handler returns to
// attach context to the request log.
//
// We use a pointer to a struct (not a context.WithValue scalar) so the
// handler can populate fields after auth without rebuilding the request's
// context — a context.WithValue clone wouldn't be visible to the
// middleware after the handler returns.
type requestMetaKey struct{}

type requestMeta struct {
	email string
	role  string
}

func requestMetaFrom(ctx context.Context) *requestMeta {
	if rm, ok := ctx.Value(requestMetaKey{}).(*requestMeta); ok {
		return rm
	}
	return nil
}

// attachAuthToRequest stores the authenticated user's email/role on the
// per-request metadata struct the HTTP middleware threads through every
// request. requireAuth calls this once it has decoded the JWT. The
// middleware reads it after the handler returns so 5xx logs include the
// email of the caller who saw the failure — the missing context that
// made the retired activity-polling endpoint's 500s undebuggable.
func attachAuthToRequest(r *http.Request, user auth.User) {
	if rm := requestMetaFrom(r.Context()); rm != nil {
		rm.email = user.Email
		rm.role = user.Role
	}
}

// instrumentedResponseWriter wraps http.ResponseWriter to capture status
// code and, on 5xx, a bounded slice of the response body so the
// middleware can extract the `detail` field for structured logging.
//
// We only buffer body bytes once we've seen a 5xx status — happy-path
// requests pay zero allocation overhead beyond the wrapper itself.
type instrumentedResponseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	bodyCapture *bytes.Buffer
}

// maxBodyCaptureBytes caps how much of a 5xx response body we buffer for
// the slog. The detail field is rarely more than a few hundred bytes.
const maxBodyCaptureBytes = 4 * 1024

func (w *instrumentedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 500 {
		w.bodyCapture = &bytes.Buffer{}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *instrumentedResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	if w.bodyCapture != nil && w.bodyCapture.Len() < maxBodyCaptureBytes {
		remaining := maxBodyCaptureBytes - w.bodyCapture.Len()
		if len(p) <= remaining {
			w.bodyCapture.Write(p)
		} else {
			w.bodyCapture.Write(p[:remaining])
		}
	}
	return n, err
}

// Flush forwards the call to the wrapped writer when it supports it.
// SSE handlers depend on Flush; without this passthrough the wrapper
// silently breaks every streaming endpoint.
func (w *instrumentedResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards to the wrapped writer's Hijacker (WebSocket upgrades on
// the terminal proxy and the SSE-but-not-quite paths). Without this the
// gorilla/coder websocket libraries can't take over the connection and
// every terminal upgrade returns 500.
func (w *instrumentedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("response writer does not support hijacking")
}

// statusClass buckets a status code into "2xx"/"3xx"/"4xx"/"5xx" so the
// counter cardinality stays at routes * methods * 4 instead of routes *
// methods * ~50 actual status codes seen in practice.
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "unknown"
	}
}

// routeFromRequest returns the Go 1.22 ServeMux pattern the request
// matched ("GET /api/sessions/{session_id}"), or "<unmatched>" if no
// pattern is associated. The pattern label is the cardinality-bounded
// alternative to logging raw URL paths (which would explode on every
// distinct session_id).
func routeFromRequest(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "<unmatched>"
}

// httpInstrumentationMiddleware wraps the orchestrator mux to record
// request/duration metrics and emit a structured slog.Error on every 5xx.
// The 5xx logline carries method, route, status, email (when the handler
// authenticated), duration_ms, and the response body's `detail` field —
// enough to root-cause without an SSH session into the pod.
//
// Cardinality budget (per docs/observability.md):
//   - tank_http_requests_total: routes * methods * 4 (status classes)
//   - tank_http_request_duration_seconds: routes * methods * 11 buckets
//
// status_class is deliberately omitted from the duration histogram —
// adding it would 4x the histogram series with no operational signal,
// since 4xx/5xx latency rarely tells a different story than 2xx latency
// for this app's request surface.
func httpInstrumentationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := &requestMeta{}
		ctx := context.WithValue(r.Context(), requestMetaKey{}, meta)
		r = r.WithContext(ctx)

		iw := &instrumentedResponseWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(iw, r)
		duration := time.Since(start)

		status := iw.status
		if status == 0 {
			status = http.StatusOK
		}
		route := routeFromRequest(r)
		method := r.Method
		class := statusClass(status)

		httpRequestsTotal.WithLabelValues(method, route, class).Inc()
		httpRequestDurationSeconds.WithLabelValues(method, route).Observe(duration.Seconds())

		if status >= 500 {
			logServerError(r, method, route, status, duration, meta, iw.bodyCapture)
		}
	})
}

func logServerError(r *http.Request, method, route string, status int, duration time.Duration, meta *requestMeta, body *bytes.Buffer) {
	detail := extractDetailField(body)
	attrs := []any{
		"method", method,
		"route", route,
		"status", status,
		"duration_ms", duration.Milliseconds(),
	}
	if meta != nil && meta.email != "" {
		attrs = append(attrs, "email", meta.email)
	}
	if meta != nil && meta.role != "" {
		attrs = append(attrs, "role", meta.role)
	}
	if detail != "" {
		attrs = append(attrs, "detail", detail)
	}
	// Some endpoints get hit unauthenticated by browsers (preflights,
	// SSE reconnects before token refresh, etc.). Logging the user-agent
	// helps separate "real user-visible 5xx" from "noisy bot" without
	// adding a metric label.
	if ua := r.UserAgent(); ua != "" {
		attrs = append(attrs, "user_agent", ua)
	}
	slog.Error("http server error", attrs...)
}

// extractDetailField pulls the "detail" string out of a JSON 5xx body the
// orchestrator writes via writeError. If the body isn't JSON or doesn't
// carry a detail, we return the raw body trimmed to its first line. This
// catches both writeError() and any handler that uses http.Error()
// directly so the slog line is useful regardless of the response shape.
func extractDetailField(body *bytes.Buffer) string {
	if body == nil || body.Len() == 0 {
		return ""
	}
	raw := body.Bytes()
	var probe struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil {
		if probe.Detail != "" {
			return probe.Detail
		}
		if probe.Error != "" {
			return probe.Error
		}
	}
	first := raw
	if idx := bytes.IndexByte(first, '\n'); idx >= 0 {
		first = first[:idx]
	}
	return string(bytes.TrimSpace(first))
}
