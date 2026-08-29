// Package logging builds the logger each service uses, so the three of them
// agree on format and level without repeating the setup, and carries a trace
// id from the edge of the system to the far end of it.
//
// Not OpenTelemetry, deliberately. Real distributed tracing wants a collector,
// storage and a UI -- three more moving parts for three services on one box.
// A trace id threaded through structured logs answers the same question, and
// it reaches somewhere OTel never would: the id travels on into the MQTT topic
// and gets logged by the firmware, so one grep spans four processes and two
// languages.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TraceHeader carries the id between services. Reusing the conventional name
// means Caddy, curl and anything else already know what it is.
const TraceHeader = "X-Request-Id"

// An unexported key type, so nothing else can collide with this in a context.
type traceKey struct{}

func WithTrace(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}

// Trace returns the id carried by ctx, or "" outside a request.
func Trace(ctx context.Context) string {
	id, _ := ctx.Value(traceKey{}).(string)

	return id
}

// traceHandler stamps the trace id onto every record logged with a context,
// so no call site has to remember to pass it.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := Trace(ctx); id != "" {
		r.AddAttrs(slog.String("trace", id))
	}

	return h.Handler.Handle(ctx, r)
}

// Both of these have to rewrap, or the first call to Logger.With unwraps the
// handler and silently stops adding trace ids.
func (h traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(as)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// New returns a logger writing to stdout, where Docker collects it.
//
// JSON is the default because these logs are read through `docker compose
// logs`, and structured output is what makes them answerable with jq rather
// than grep. Text stays available for reading them by eye during development.
func New(format, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}

	opts := &slog.HandlerOptions{Level: lvl}

	switch format {
	case "json":
		return Wrap(slog.NewJSONHandler(os.Stdout, opts)), nil
	case "text":
		return Wrap(slog.NewTextHandler(os.Stdout, opts)), nil
	default:
		return nil, fmt.Errorf("log format %q: want json or text", format)
	}
}

// Wrap adds trace stamping to a handler built elsewhere -- a test writing to a
// buffer, mostly. New does this for the handlers it makes; a logger that skips
// it still works and simply never records a trace, which is the failure this
// exists to make hard.
func Wrap(h slog.Handler) *slog.Logger { return slog.New(traceHandler{h}) }

// Requests gives every request a trace id and logs one line when it finishes.
//
// Without this a rejected request logged only its reason -- no path, no
// status, no caller, no duration -- which is enough to know something went
// wrong and not enough to know what.
func Requests(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// An id from upstream is reused rather than replaced, so one
			// request reads as one trace across every service it touches.
			// Only the edge invents one.
			id := r.Header.Get(TraceHeader)
			if id == "" {
				id = uuid.NewString()
			}

			ctx := WithTrace(r.Context(), id)
			w.Header().Set(TraceHeader, id)

			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()

			next.ServeHTTP(rec, r.WithContext(ctx))

			// Levelled by status, so `--log-level=warn` leaves exactly the
			// requests that went wrong. Logging every request at info made a
			// 500 arrive as an ERROR line followed by an INFO one describing
			// the same request.
			level := slog.LevelInfo

			switch {
			case rec.status >= http.StatusInternalServerError:
				level = slog.LevelError
			case rec.status >= http.StatusBadRequest:
				level = slog.LevelWarn
			}

			// For a stream this lands when the client disconnects, so the
			// duration is how long the tab was open. That is the useful
			// number for a stream and the useful number for a request.
			log.Log(ctx, level, "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"ms", time.Since(start).Milliseconds(),
				"ip", ClientIP(r),
			)
		})
	}
}

// recorder remembers what the handler replied.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *recorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n

	return n, err
}

// Unwrap is what lets http.ResponseController reach the real writer
// underneath. Without it both Flush and SetWriteDeadline fail, and every
// event stream dies the instant it tries to send anything.
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Truncate bounds a string that came from somewhere else -- a printed
// message, a device payload -- before it reaches a log line. Whatever sent it
// does not get to decide how much of the log it occupies.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}

// ClientIP is the caller as seen from behind Caddy.
//
// Caddy *appends* to X-Forwarded-For, so the last entry is the one it observed
// and the earlier ones are whatever the caller chose to claim. Reading the
// first would let anyone forge their own address.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.LastIndexByte(fwd, ','); i >= 0 {
			return strings.TrimSpace(fwd[i+1:])
		}

		return strings.TrimSpace(fwd)
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
