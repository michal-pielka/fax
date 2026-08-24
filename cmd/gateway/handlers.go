package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// maxBody caps the request body before the JSON decoder sees it. Validate
// protects against a large document; only this protects against a body that
// never stops arriving.
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
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}

	// The renderer validates too, but rejecting here saves a round trip and
	// keeps the public error messages under this service's control.
	if err := d.Validate(a.limits); err != nil {
		if errors.Is(err, doc.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		a.log.Error("validate", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	// Render before touching the dispatcher.
	payload, err := a.renderer.Render(r.Context(), d)
	if err != nil {
		a.upstreamFailed(w, "renderer", err)
		return
	}

	// Not stored anywhere: the id appears in the log line and travels to the
	// firmware in the MQTT topic, so a redelivered job can be recognised
	// rather than printed twice.
	id := uuid.NewString()

	if err := a.dispatcher.Print(r.Context(), id, payload); err != nil {
		a.upstreamFailed(w, "dispatcher", err)
		return
	}

	a.log.Info("print accepted", "id", id, "chars", len(d.Text), "bytes", len(payload))

	// "published" rather than "printed": the broker has the job, but the
	// firmware cannot yet confirm that paper moved. This becomes "printed"
	// when the dispatcher waits for a device acknowledgement.
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "published"})
}

func (a *api) state(w http.ResponseWriter, r *http.Request) {
	st, err := a.dispatcher.State(r.Context())
	if err != nil {
		a.upstreamFailed(w, "dispatcher", err)
		return
	}

	writeJSON(w, http.StatusOK, st)
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// passThrough lists the statuses an internal service is trusted to have chosen
// deliberately -- they describe the printer or the request, not a fault:
//
//	400 the renderer rejected the document
//	409 out of paper
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

func (a *api) upstreamFailed(w http.ResponseWriter, service string, err error) {
	var ue *upstreamError
	if errors.As(err, &ue) && passThrough[ue.Status] {
		writeError(w, ue.Status, ue.Msg)
		return
	}

	a.log.Error("upstream failed", "service", service, "err", err)
	writeError(w, http.StatusBadGateway, service+" is unavailable")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
