// Package logging gives the three services one logger and one trace id. Not
// OpenTelemetry: no collector needed, and this one reaches the firmware.
package logging

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TraceHeader carries the id between services, under a name Caddy and curl
// already understand.
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

// New logs to stdout, where Docker collects it. JSON so the result is
// answerable with jq; text for reading by eye during development.
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

// Flags are the two logging flags every service takes.
type Flags struct {
	format, level *string
}

// RegisterFlags adds -log-format and -log-level to the default flag set. Call
// before flag.Parse, then Logger after it.
func RegisterFlags() Flags {
	return Flags{
		format: flag.String("log-format", "json", "log format: json or text"),
		level:  flag.String("log-level", "info", "log level: debug, info, warn or error"),
	}
}

func (f Flags) Logger() (*slog.Logger, error) { return New(*f.format, *f.level) }

// Wrap adds trace stamping to a handler built elsewhere, a test buffer mostly.
// A logger that skips it works and silently never records a trace.
func Wrap(h slog.Handler) *slog.Logger { return slog.New(traceHandler{h}) }

// Requests gives every request a trace id and one line when it finishes.
// Without it a rejection logged its reason and nothing else.
func Requests(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reused, not replaced: only the edge invents one.
			id := r.Header.Get(TraceHeader)
			if id == "" {
				id = uuid.NewString()
			}

			ctx := WithTrace(r.Context(), id)
			w.Header().Set(TraceHeader, id)

			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()

			next.ServeHTTP(rec, r.WithContext(ctx))

			// Levelled by status, so --log-level=warn leaves exactly the
			// requests that went wrong.
			level := slog.LevelInfo

			switch {
			case rec.status >= http.StatusInternalServerError:
				level = slog.LevelError
			case rec.status >= http.StatusBadRequest:
				level = slog.LevelWarn
			}

			// For a stream this lands on disconnect, so the duration is how
			// long the tab was open.
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

// Unwrap lets http.ResponseController reach the real writer, so Flush and
// SetWriteDeadline work through this wrapper rather than failing silently.
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Truncate bounds a string from elsewhere, so whatever sent it does not decide
// how much of the log it occupies.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}

// ClientIP reads the LAST X-Forwarded-For entry: Caddy appends, so earlier
// ones are whatever the caller chose to claim.
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
