package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/httpx"
	"github.com/michal-pielka/fax/server/internal/logging"
	"github.com/michal-pielka/fax/server/internal/wire"
)

// maxBody caps the body before anything reads it. Validate stops a large
// document; only this stops one that never stops arriving. A dithered square
// photo is under 20 KB as PNG, so the same cap serves both kinds.
const maxBody = 64 << 10 // 64 KiB

// printTimeout is how long to wait for the dispatcher: its own ackTimeout,
// which allows the payload its wire time twice, plus three seconds of ours.
// Text is a few hundred bytes and waits about ten seconds at most; a square
// photo is 18 KB and may wait close to fifty.
func printTimeout(payload int) time.Duration {
	return 2*wire.Time(payload) + 10*time.Second
}

type api struct {
	renderer   Renderer
	dispatcher Dispatcher
	limits     doc.Limits
	// photos is a directory to keep a copy of every picture printed, or
	// empty. Text is logged in full; this is the same courtesy for pictures.
	photos string
	log    *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/print", a.print)
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /api/health", httpx.Health)

	return mux
}

// print is one door with two shapes of parcel. The content type says which:
// a JSON document, or a PNG that is already the printer's bitmap. Anything
// else is refused rather than guessed at.
func (a *api) print(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))

	switch ct {
	case "application/json":
		a.printText(w, r)
	case "image/png":
		a.printPhoto(w, r)
	default:
		a.fail(w, r, http.StatusUnsupportedMediaType, "send application/json or image/png")
	}
}

func (a *api) printText(w http.ResponseWriter, r *http.Request) {
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

	// The message is logged on purpose: it is about to be printed and read
	// anyway, and nothing else records what a stranger sent.
	a.send(w, r, payload, "kind", "text",
		"chars", utf8.RuneCountInString(d.Text),
		"text", logging.Truncate(d.Text, 512),
	)
}

// printPhoto takes a PNG that the browser has already scaled to the paper and
// dithered. Only the header is read here; the renderer decodes the rest once
// the size is known good.
func (a *api) printPhoto(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, "photo too large or unreadable")
		return
	}

	cfg, err := doc.ValidatePhoto(bytes.NewReader(b))
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	a.keep(r.Context(), b)

	payload, err := a.renderer.RenderPhoto(r.Context(), b)
	if err != nil {
		a.upstreamFailed(w, r, "renderer", err)
		return
	}

	a.send(w, r, payload, "kind", "photo", "rows", cfg.Height)
}

// send is the half of printing that does not care what is being printed:
// hand the bytes to the dispatcher, wait for the printer's answer, report.
func (a *api) send(w http.ResponseWriter, r *http.Request, payload []byte, attrs ...any) {
	// The trace id is the job id, so one grep follows a receipt from here to
	// the firmware. Stored nowhere: it lives as long as the request.
	id := logging.Trace(r.Context())

	// Blocks until the firmware has answered for the job. For text that is
	// about a second; for a picture, however long its bytes take on the wire.
	ctx, cancel := context.WithTimeout(r.Context(), printTimeout(len(payload)))
	defer cancel()

	if err := a.dispatcher.Print(ctx, id, payload); err != nil {
		a.upstreamFailed(w, r, "dispatcher", err)
		return
	}

	a.log.InfoContext(r.Context(), "printed", append([]any{"bytes", len(payload)}, attrs...)...)

	// 200: the printer took the bytes and answered with paper in. The id is
	// the reference printed on the receipt and the trace in the logs.
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"id": id, "status": "printed"})
}

// keep writes the picture to the photos directory under its trace id. A
// failure is logged and ignored: the trail matters, the print more.
func (a *api) keep(ctx context.Context, png []byte) {
	if a.photos == "" {
		return
	}

	name := filepath.Join(a.photos, logging.Trace(ctx)+".png")
	if err := os.WriteFile(name, png, 0o644); err != nil {
		a.log.ErrorContext(ctx, "cannot keep photo", "path", name, "err", err)
	}
}

func (a *api) state(w http.ResponseWriter, r *http.Request) {
	st, err := a.dispatcher.State(r.Context())
	if err != nil {
		a.upstreamFailed(w, r, "dispatcher", err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, st)
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

// fail writes an error response and records it. 4xx is the caller's fault and
// routine, so it warns; 5xx is ours and errors.
func (a *api) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	level := slog.LevelWarn
	if code >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	// The middleware's request line carries everything but the reason.
	a.log.Log(r.Context(), level, "rejected", "reason", msg)

	httpx.WriteJSON(w, code, map[string]string{"error": msg})
}
