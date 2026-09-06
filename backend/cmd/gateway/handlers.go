package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/logging"
)

// maxBody caps the body before the decoder sees it. Validate stops a large
// document; only this stops one that never stops arriving.
const maxBody = 64 << 10 // 64 KiB

type api struct {
	renderer   Renderer
	dispatcher Dispatcher
	limits     doc.Limits
	log        *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/print", a.print)
	mux.HandleFunc("GET /api/state", a.state)
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

		a.log.ErrorContext(r.Context(), "validator failed", "err", err)
		a.fail(w, r, http.StatusInternalServerError, "internal error")

		return
	}

	// Render before touching the dispatcher.
	payload, err := a.renderer.Render(r.Context(), d)
	if err != nil {
		a.upstreamFailed(w, r, "renderer", err)
		return
	}

	// The trace id is the job id, so one grep follows a receipt from here to
	// the firmware. Stored nowhere: it lives as long as the request.
	id := logging.Trace(r.Context())

	// Blocks until the firmware has answered for the job: about a second of
	// UART time and two broker hops. The errors below are that answer.
	if err := a.dispatcher.Print(r.Context(), id, payload); err != nil {
		a.upstreamFailed(w, r, "dispatcher", err)
		return
	}

	// The message is logged on purpose: it is about to be printed and read
	// anyway, and nothing else records what a stranger sent.
	a.log.InfoContext(r.Context(), "printed",
		"chars", utf8.RuneCountInString(d.Text),
		"bytes", len(payload),
		"text", logging.Truncate(d.Text, 512),
	)

	// 200: the printer took the bytes and answered with paper in. The id is
	// the reference printed on the receipt and the trace in the logs.
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

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// passThrough are statuses an internal service chose deliberately: 400 bad
// document, 409 no paper or busy, 503 offline, 504 no confirmation from the
// printer. Anything else is a 502.
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
	a.log.ErrorContext(r.Context(), "upstream failed", "service", service, "err", err)
	a.fail(w, r, http.StatusBadGateway, service+" is unavailable")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes an error response and records it. 4xx is the caller's fault and
// routine, so it warns; 5xx is ours and errors.
func (a *api) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	level := slog.LevelWarn
	if code >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	// The middleware's request line carries everything but the reason.
	a.log.Log(r.Context(), level, "rejected", "reason", msg)

	writeJSON(w, code, map[string]string{"error": msg})
}
