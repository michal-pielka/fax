package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/render"
)

// maxBody caps the body before anything reads it. Validate stops a large
// document; only this stops one that never stops arriving. A dithered square
// photo is under 20 KB as PNG, so the same cap serves both kinds.
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

// render is one route with two bodies. The content type says which: a JSON
// document, or a PNG that is already the printer's bitmap. Nothing else.
func (a *api) render(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))

	switch ct {
	case "application/json":
		a.renderText(w, r)
	case "image/png":
		a.renderPhoto(w, r)
	default:
		writeError(w, http.StatusUnsupportedMediaType, "send application/json or image/png")
	}
}

func (a *api) renderText(w http.ResponseWriter, r *http.Request) {
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

// renderPhoto trusts nothing about the bytes until the header has been read:
// the size check bounds what the decoder may allocate before it runs.
func (a *api) renderPhoto(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "photo too large or unreadable")
		return
	}

	cfg, err := doc.ValidatePhoto(bytes.NewReader(b))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a PNG: "+err.Error())
		return
	}

	payload := render.RenderPhoto(img)
	a.log.DebugContext(r.Context(), "rendered", "rows", cfg.Height, "bytes", len(payload))

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
