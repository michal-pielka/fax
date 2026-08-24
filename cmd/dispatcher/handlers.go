package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// maxBody caps the request body. A rendered receipt is a few hundred bytes;
// this is generous enough for a raster image later and still bounded.
const maxBody = 1 << 20 // 1 MiB

type api struct {
	printer Printer
	log     *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Internal only: the gateway is the sole caller. Nothing here should ever
	// be reachable from outside, since it publishes straight to the printer.
	mux.HandleFunc("POST /internal/print", a.print)
	mux.HandleFunc("GET /internal/state", a.state)
	mux.HandleFunc("GET /internal/health", a.health)

	return mux
}

type printRequest struct {
	// ID travels to the firmware in the topic so a redelivery can be
	// recognised and dropped.
	ID string `json:"id"`
	// Payload is opaque here. The dispatcher deliberately knows nothing about
	// ESC/POS -- it moves bytes.
	Payload []byte `json:"payload"`
}

func (a *api) print(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	var req printRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}

	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	if len(req.Payload) == 0 {
		writeError(w, http.StatusBadRequest, "empty payload")
		return
	}

	// The request context carries through to the publish, so a gateway that
	// gives up -- because its own caller closed the tab -- does not leave this
	// blocked on a broker that is not answering.
	err := a.printer.Publish(r.Context(), req.ID, req.Payload)

	switch {
	case err == nil:
		// Deliberately "published", not "printed": the broker has the job.
		// Whether paper moved is something the firmware cannot yet report.
		writeJSON(w, http.StatusOK, map[string]string{"id": req.ID, "status": "published"})

	case errors.Is(err, ErrNoPaper):
		writeError(w, http.StatusConflict, err.Error())

	case errors.Is(err, ErrOffline):
		writeError(w, http.StatusServiceUnavailable, err.Error())

	default:
		// Reaching the broker is the dispatcher's job, so failing to is the
		// dispatcher's fault, not a statement about the printer.
		a.log.Error("publish failed", "id", req.ID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "cannot reach the broker")
	}
}

func (a *api) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.printer.State())
}

// health reports on the dispatcher, not the printer. An offline printer is a
// normal condition and must not make this process look unhealthy, or an
// orchestrator would restart it pointlessly.
func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
