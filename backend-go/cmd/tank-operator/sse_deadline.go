package main

import (
	"net/http"
	"time"
)

// sseDeadlineWriter arms a per-write deadline on the underlying connection
// before every SSE write (issue #1077: the streams had no write deadlines —
// a hung client pinned the handler goroutine and its connection forever; a
// slow reader blocked the write loop indefinitely). The deadline rides the
// conn, so it also bounds the flush that follows each write. After a
// deadline fires the connection is poisoned by design: the stream ends and
// the SPA's reconnect-with-backoff (#1093) takes over.
//
// SetWriteDeadline is best-effort: ResponseController returns
// ErrNotSupported for exotic wrappers, and erroring the stream for THAT
// would break environments the stream otherwise works in — so failures to
// arm are ignored, preserving pre-deadline behavior there.
type sseDeadlineWriter struct {
	http.ResponseWriter
	rc      *http.ResponseController
	flusher http.Flusher
	timeout time.Duration
}

// sseWriteTimeout bounds one SSE write+flush round.
var sseWriteTimeout = time.Duration(envInt("SSE_WRITE_TIMEOUT_SECONDS", 30)) * time.Second

// newSSEDeadlineWriter wraps an SSE handler's ResponseWriter. The returned
// writer implements http.Flusher (flushing through the original writer) so
// existing flusher call sites keep working against the wrapper.
func newSSEDeadlineWriter(w http.ResponseWriter, flusher http.Flusher) *sseDeadlineWriter {
	return &sseDeadlineWriter{
		ResponseWriter: w,
		rc:             http.NewResponseController(w),
		flusher:        flusher,
		timeout:        sseWriteTimeout,
	}
}

func (d *sseDeadlineWriter) Write(p []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(d.timeout))
	return d.ResponseWriter.Write(p)
}

func (d *sseDeadlineWriter) Flush() {
	_ = d.rc.SetWriteDeadline(time.Now().Add(d.timeout))
	d.flusher.Flush()
}
