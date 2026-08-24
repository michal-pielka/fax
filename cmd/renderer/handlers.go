package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/render"
)

// maxBody caps the request body before the JSON decoder sees it. Validate
// protects against a large document; only this protects against a body that
// never stops arriving.
const maxBody = 64 << 10 // 64 KiB

type api struct {
	limits doc.Limits
	log    *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Nothing here is public -- the gateway is the only caller -- so the paths
	// are marked internal to make that obvious in logs and proxy configs.
	mux.HandleFunc("POST /internal/render", a.render)
	mux.HandleFunc("GET /internal/health", a.health)

	return mux
}

type renderResponse struct {
	// Payload is the ESC/POS byte stream. encoding/json base64s a []byte, so
	// it survives the trip intact -- these bytes are not valid UTF-8, and a
	// string field would have replaced the high ones with U+FFFD.
	Payload []byte `json:"payload"`
}

func (a *api) render(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	var d doc.Document
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a typo'd field is a 400, not silent data loss

	if err := dec.Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}

	if err := d.Validate(a.limits); err != nil {
		if errors.Is(err, doc.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		a.log.Error("validate", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	payload := render.Render(d)
	a.log.Info("rendered", "chars", len(d.Text), "spans", len(d.Spans), "bytes", len(payload))

	writeJSON(w, http.StatusOK, renderResponse{Payload: payload})
}

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
