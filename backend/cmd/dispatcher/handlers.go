package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/michal-pielka/fax/server/internal/httpx"
)

// maxBody caps the request body. A text receipt is a few hundred bytes and a
// square photo about 19 KB; a megabyte is bounded and nowhere near either.
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
	mux.HandleFunc("GET /internal/health", httpx.Health)

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
		httpx.WriteError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}

	if req.ID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "missing id")
		return
	}

	if len(req.Payload) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "empty payload")
		return
	}

	// Blocks until the firmware answers. The request context reaches the
	// publish, so a gateway that gives up does not leave this waiting on a
	// silent broker -- but the printer stays claimed until the ack.
	err := a.printer.Publish(r.Context(), req.ID, req.Payload)

	switch {
	case err == nil:
		// 200: the printer has the bytes and had paper when it took them.
		// The paper itself is still moving for a few seconds after this.
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"id": req.ID, "status": "printed"})

	case errors.Is(err, ErrNoPaper), errors.Is(err, ErrBusy):
		// Both describe the printer's current condition rather than a fault,
		// and both become printable again on their own.
		httpx.WriteError(w, http.StatusConflict, err.Error())

	case errors.Is(err, ErrOffline):
		httpx.WriteError(w, http.StatusServiceUnavailable, err.Error())

	case errors.Is(err, ErrNoConfirmation):
		// The one status that means "unknown": the bytes went out and no
		// answer came back in time. Not 500, since nothing here failed.
		httpx.WriteError(w, http.StatusGatewayTimeout, err.Error())

	default:
		// Reaching the broker is the dispatcher's job, so failing to is the
		// dispatcher's fault, not a statement about the printer.
		a.log.ErrorContext(r.Context(), "publish failed", "err", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "cannot reach the broker")
	}
}

func (a *api) state(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, a.printer.State())
}
