package logging

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAcceptsValidCombinations(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			if _, err := New(format, level); err != nil {
				t.Errorf("New(%q, %q) = %v", format, level, err)
			}
		}
	}
}

// A typo in a flag should stop the service at startup with a message naming
// the valid values, rather than silently defaulting and hiding logs.
func TestRejectsGarbage(t *testing.T) {
	if _, err := New("yaml", "info"); err == nil {
		t.Error("bad format accepted")
	}

	if _, err := New("json", "verbose"); err == nil {
		t.Error("bad level accepted")
	}
}

// Caddy appends to X-Forwarded-For rather than replacing it, so a caller can
// prepend anything. Trusting the first entry is how spoofed addresses get into
// logs; the last one is the address Caddy actually saw.
func TestClientIPIgnoresSpoofedPrefix(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")

	if got := ClientIP(r); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the last entry 203.0.113.9", got)
	}
}

func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.7:54321"

	if got := ClientIP(r); got != "198.51.100.7" {
		t.Errorf("ClientIP = %q, want 198.51.100.7", got)
	}
}

// A record logged with a request's context has to carry the trace, or the
// whole scheme is just a header nobody reads.
func TestTraceReachesTheRecord(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(traceHandler{slog.NewTextHandler(&out, nil)})

	log.InfoContext(WithTrace(context.Background(), "abc123"), "hello")

	if !strings.Contains(out.String(), "trace=abc123") {
		t.Errorf("logged %q, want a trace attribute", out.String())
	}
}

// Logger.With returns a new handler. Without rewrapping, the very first .With
// in the codebase would silently stop stamping trace ids.
func TestTraceSurvivesWith(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(traceHandler{slog.NewTextHandler(&out, nil)}).With("service", "test")

	log.InfoContext(WithTrace(context.Background(), "abc123"), "hello")

	if !strings.Contains(out.String(), "trace=abc123") {
		t.Errorf("logged %q, want a trace attribute after With", out.String())
	}
}

func TestRequestsReusesAnIncomingTrace(t *testing.T) {
	var seen string
	var out bytes.Buffer

	h := Requests(slog.New(traceHandler{slog.NewTextHandler(&out, nil)}))(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = Trace(r.Context())
		}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(TraceHeader, "from-upstream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Reused, not replaced: one request has to read as one trace across every
	// service it passes through.
	if seen != "from-upstream" {
		t.Errorf("handler saw trace %q, want from-upstream", seen)
	}

	if got := rec.Header().Get(TraceHeader); got != "from-upstream" {
		t.Errorf("response header = %q", got)
	}

	if !strings.Contains(out.String(), "status=200") {
		t.Errorf("request line = %q, want a status", out.String())
	}
}

func TestRequestsInventsATraceAtTheEdge(t *testing.T) {
	var seen string

	h := Requests(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = Trace(r.Context())
		}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if seen == "" {
		t.Error("no trace id was created for a request that arrived without one")
	}
}

// The recorder wraps the ResponseWriter, and http.ResponseController can only
// find Flush and SetWriteDeadline through Unwrap. Without it every event
// stream dies the instant it tries to send.
func TestRecorderStaysControllable(t *testing.T) {
	var flushed bool

	h := Requests(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("Flush through the recorder: %v", err)
				return
			}

			flushed = true
		}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if !flushed {
		t.Error("could not flush through the recorder")
	}
}

// The status has to be recorded even when the handler never calls WriteHeader.
func TestRecorderDefaultsTo200(t *testing.T) {
	var out bytes.Buffer

	h := Requests(slog.New(slog.NewTextHandler(&out, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("hi"))
		}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if !strings.Contains(out.String(), "status=200") {
		t.Errorf("request line = %q", out.String())
	}
}
