package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/sse"
)

// maxBody caps the request body before the JSON decoder sees it. Validate
// protects against a large document; only this protects against a body that
// never stops arriving.
const maxBody = 64 << 10 // 64 KiB

type api struct {
	renderer   Renderer
	dispatcher Dispatcher
	hub        *hub
	limits     doc.Limits
	log        *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/print", a.print)
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("GET /api/health", a.health)

	return mux
}

func (a *api) print(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	var d doc.Document
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a typo'd field is a 400, not silent data loss

	if err := dec.Decode(&d); err != nil {
		a.fail(w, r, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}

	// The renderer validates too, but rejecting here saves a round trip and
	// keeps the public error messages under this service's control.
	if err := d.Validate(a.limits); err != nil {
		if errors.Is(err, doc.ErrInvalid) {
			a.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}

		a.log.Error("validate", "err", err)
		a.fail(w, r, http.StatusInternalServerError, "internal error")

		return
	}

	// Render before touching the dispatcher.
	payload, err := a.renderer.Render(r.Context(), d)
	if err != nil {
		a.upstreamFailed(w, r, "renderer", err)
		return
	}

	// Not stored anywhere: the id appears in the log line and travels to the
	// firmware in the MQTT topic, so a redelivered job can be recognised
	// rather than printed twice.
	id := uuid.NewString()

	if err := a.dispatcher.Print(r.Context(), id, payload); err != nil {
		a.upstreamFailed(w, r, "dispatcher", err)
		return
	}

	a.log.Info("print accepted", "id", id, "ip", clientIP(r), "chars", len(d.Text), "bytes", len(payload))

	// "printed" is now literal. Print only returns nil once the firmware has
	// said the paper moved, so this request lasted as long as the receipt did.
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "printed"})
}

func (a *api) state(w http.ResponseWriter, r *http.Request) {
	st, err := a.dispatcher.State(r.Context())
	if err != nil {
		a.upstreamFailed(w, r, "dispatcher", err)
		return
	}

	writeJSON(w, http.StatusOK, st)
}

// eventHeartbeat is how often a silent stream sends a comment, so that phone
// radios and NAT tables do not reap a connection for being quiet.
const eventHeartbeat = 20 * time.Second

// events streams the printer's state to a browser for as long as the tab is
// open.
//
// The chain behind it never polls: the firmware publishes a change, the
// dispatcher notices in its MQTT callback, the gateway is already holding a
// stream open to hear about it, and this pushes it out. A lamp changes because
// something happened, not because a timer went off.
func (a *api) events(w http.ResponseWriter, r *http.Request) {
	changes, current, unsubscribe, ok := a.hub.subscribe()
	if !ok {
		a.fail(w, r, http.StatusServiceUnavailable, "too many open streams")
		return
	}
	defer unsubscribe()

	stream, err := sse.Start(w)
	if err != nil {
		a.log.Error("cannot start event stream", "err", err, "ip", clientIP(r))
		return
	}

	// Send at once, so the page never opens with the lamps dark waiting for
	// something to change.
	if err := stream.Send(current); err != nil {
		return
	}

	ping := time.NewTicker(eventHeartbeat)
	defer ping.Stop()

	for {
		var err error

		select {
		case <-r.Context().Done():
			return

		case s := <-changes:
			err = stream.Send(s)

		case <-ping.C:
			err = stream.Ping()
		}

		// A write that fails means the tab is gone. Nothing to log and nobody
		// left to tell.
		if err != nil {
			return
		}
	}
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// passThrough lists the statuses an internal service is trusted to have chosen
// deliberately -- they describe the printer or the request, not a fault:
//
//	400 the renderer rejected the document
//	409 out of paper, or already printing something else
//	503 printer offline, or the broker is unreachable
//	504 sent, but never acknowledged
//
// Anything else means that service is itself broken, which is a 502: the
// caller did nothing wrong, and the same request may well work later.
var passThrough = map[int]bool{
	http.StatusBadRequest:         true,
	http.StatusConflict:           true,
	http.StatusServiceUnavailable: true,
	http.StatusGatewayTimeout:     true,
}

func (a *api) upstreamFailed(w http.ResponseWriter, r *http.Request, service string, err error) {
	var ue *upstreamError
	if errors.As(err, &ue) && passThrough[ue.Status] {
		a.fail(w, r, ue.Status, ue.Msg)
		return
	}

	// The underlying error only appears here; fail() records the response.
	a.log.Error("upstream failed", "service", service, "err", err)
	a.fail(w, r, http.StatusBadGateway, service+" is unavailable")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes an error response and records it.
//
// Rejections used to be silent, which left the two questions you actually have
// about a public endpoint unanswerable: what are people sending that gets
// turned away, and how often is the printer unreachable.
//
// 4xx is the caller's fault and routine, so it warns; 5xx is ours and errors.
func (a *api) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	level := slog.LevelWarn
	if code >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	a.log.Log(r.Context(), level, "rejected",
		"status", code,
		"reason", msg,
		"ip", clientIP(r),
		"path", r.URL.Path,
	)

	writeJSON(w, code, map[string]string{"error": msg})
}

// clientIP is the address the request actually came from.
//
// Caddy sits in front, so RemoteAddr is always Caddy. Caddy *appends* to
// X-Forwarded-For rather than replacing it, which means a caller can prepend
// whatever they like -- a client sending "X-Forwarded-For: 1.2.3.4" arrives
// here as "1.2.3.4, <their real address>".
//
// So the last entry is the trustworthy one, and the widespread habit of taking
// the first is how spoofed addresses end up in logs and rate limiters.
func clientIP(r *http.Request) string {
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
