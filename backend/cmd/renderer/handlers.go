package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/render"
)

// maxBody caps the body before the decoder sees it. Validate stops a large
// document; only this stops one that never stops arriving.
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
	// []byte so encoding/json base64s it: ESC/POS is not valid UTF-8, and a
	// string field would replace the high bytes with U+FFFD.
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

		a.log.ErrorContext(r.Context(), "validator failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")

		return
	}

	payload := render.Render(d)
	// Debug: the request line already covers timing and size. This adds the
	// document's shape, which matters when a receipt looks wrong.
	a.log.DebugContext(r.Context(), "rendered",
		"chars", utf8.RuneCountInString(d.Text),
		"spans", len(d.Spans),
		"bytes", len(payload),
	)

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
