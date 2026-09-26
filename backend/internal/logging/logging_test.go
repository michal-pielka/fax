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

// Caddy appends to X-Forwarded-For, so a caller can prepend anything.
// Trusting the first entry is how spoofed addresses get into logs.
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

// ResponseController finds Flush only through Unwrap. Without it every event
// stream dies on its first send.
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

// A 500 arriving as ERROR from the handler and INFO from the middleware made
// --log-level=warn hide half the story of the same request.
func TestRequestLineIsLevelledByStatus(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusOK, "level=INFO"},
		{http.StatusBadRequest, "level=WARN"},
		{http.StatusInternalServerError, "level=ERROR"},
	} {
		var out bytes.Buffer

		h := Requests(slog.New(slog.NewTextHandler(&out, nil)))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))

		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

		if !strings.Contains(out.String(), tt.want) {
			t.Errorf("status %d logged %q, want %s", tt.status, out.String(), tt.want)
		}
	}
}

// Whatever sent the string does not get to decide how much of the log it
// occupies.
func TestTruncate(t *testing.T) {
	if got := Truncate("short", 32); got != "short" {
		t.Errorf("Truncate kept %q", got)
	}

	if got := Truncate(strings.Repeat("x", 100), 10); got != strings.Repeat("x", 10)+"..." {
		t.Errorf("Truncate = %q", got)
	}

	// "é" is two bytes; cutting between them would log invalid UTF-8.
	if got := Truncate("aé", 2); got != "a..." {
		t.Errorf("Truncate split a rune: %q", got)
	}
}

// The message is attacker controlled and goes straight into the log, so a
// newline in it must not be able to forge a second line.
func TestMessagesCannotForgeALogLine(t *testing.T) {
	var out bytes.Buffer

	Wrap(slog.NewTextHandler(&out, nil)).
		Info("print accepted", "text", "hello\nlevel=ERROR msg=\"forged\"")

	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("a newline escaped into the log: %q", out.String())
	}
}
