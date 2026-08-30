package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/michal-pielka/fax/server/internal/sse"
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
	mux.HandleFunc("GET /internal/events", a.events)
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

	// The request context reaches the publish, so a gateway that gives up
	// does not leave this blocked on a silent broker.
	err := a.printer.Publish(r.Context(), req.ID, req.Payload)

	switch {
	case err == nil:
		// 202: the broker has the job and the printer is claimed. How it ends
		// arrives on the event stream, tagged with this id.
		writeJSON(w, http.StatusAccepted, map[string]string{"id": req.ID, "status": "accepted"})

	case errors.Is(err, ErrNoPaper), errors.Is(err, ErrBusy):
		// Both describe the printer's current condition rather than a fault,
		// and both become printable again on their own.
		writeError(w, http.StatusConflict, err.Error())

	case errors.Is(err, ErrOffline):
		writeError(w, http.StatusServiceUnavailable, err.Error())

	default:
		// Reaching the broker is the dispatcher's job, so failing to is the
		// dispatcher's fault, not a statement about the printer.
		a.log.ErrorContext(r.Context(), "publish failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "cannot reach the broker")
	}
}

// stateResponse adds the two things the device does not publish: whether a job
// is on the printer, and how the last one ended. Both are ours to know.
type stateResponse struct {
	State
	Busy bool    `json:"busy"`
	Last *Result `json:"last,omitempty"`
}

func (a *api) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.snapshot())
}

// snapshot deliberately takes no single lock: the two halves are guarded
// separately, and holding both would impose an ordering on everything else.
func (a *api) snapshot() stateResponse {
	s := stateResponse{State: a.printer.State(), Busy: a.printer.Busy()}

	if last, ok := a.printer.LastJob(); ok {
		s.Last = &last
	}

	return s
}

// eventHeartbeat is how often a silent stream sends a comment, so an idle
// connection is not mistaken for a dead one by anything in between.
const eventHeartbeat = 20 * time.Second

// events streams state to the gateway, which fans it out. Written for any
// number of readers: a redeploy briefly has two gateways.
func (a *api) events(w http.ResponseWriter, r *http.Request) {
	changed, unsubscribe := a.printer.Subscribe()
	defer unsubscribe()

	stream, err := sse.Start(w)
	if err != nil {
		a.log.ErrorContext(r.Context(), "cannot start event stream", "err", err)
		return
	}

	// Current state first, so a reconnecting gateway is never briefly blind.
	if err := stream.Send(a.snapshot()); err != nil {
		return
	}

	ping := time.NewTicker(eventHeartbeat)
	defer ping.Stop()

	for {
		var err error

		select {
		case <-r.Context().Done():
			return

		case <-changed:
			err = stream.Send(a.snapshot())

		case <-ping.C:
			err = stream.Ping()
		}

		// Any write error means the reader is gone. There is nothing to
		// report and nobody to report it to.
		if err != nil {
			return
		}
	}
}

// health reports on the dispatcher, not the printer: an offline printer is
// normal, and must not get this process restarted.
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
