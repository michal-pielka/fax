package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/httpx"
	"github.com/michal-pielka/fax/server/internal/logging"
	"github.com/michal-pielka/fax/server/internal/timeouts"
)

// maxBody caps the body before anything reads it. Validate stops a large
// document; only this stops one that never stops arriving. A dithered square
// photo is under 20 KB as PNG, so the same cap serves both kinds.
const maxBody = 64 << 10 // 64 KiB

type api struct {
	renderer   Renderer
	dispatcher Dispatcher
	// photos is a directory to keep a copy of every picture printed, or
	// empty. Text is logged in full; this is the same courtesy for pictures.
	photos string
	// wall is the public record of every print, shown at /prints.
	wall *Wall
	log  *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/print", a.print)
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /api/health", httpx.Health)

	mux.HandleFunc("GET /api/prints", a.prints)
	mux.HandleFunc("GET /api/prints/stream", a.stream)
	mux.HandleFunc("GET /api/prints/{id}/photo", a.photo)
	// Caddy proxies only /api/*, so this is reachable from the compose
	// network alone: deploy/hide.sh calls it from the caddy container.
	mux.HandleFunc("POST /internal/prints/{id}/hide", a.hide)

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
	// The renderer validates too, but rejecting here saves a round trip and
	// keeps the public error messages under this service's control.
	d, err := doc.Decode(r.Body, doc.Paper)
	if err != nil {
		a.fail(w, r, http.StatusBadRequest, err.Error())
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
	a.send(w, r, payload, Print{Kind: "text", Text: d.Text, Spans: d.Spans}, "kind", "text",
		"chars", utf8.RuneCountInString(d.Text),
		"text", logging.Truncate(d.Text, 512),
	)
}

// printPhoto takes a PNG that the browser has already scaled to the paper and
// dithered. Only the header is read here; the renderer decodes the rest once
// the size is known good.
func (a *api) printPhoto(w http.ResponseWriter, r *http.Request) {
	b, cfg, err := doc.ReadPhoto(r.Body)
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

	a.send(w, r, payload, Print{Kind: "photo", Rows: cfg.Height}, "kind", "photo", "rows", cfg.Height)
}

// send is the half of printing that does not care what is being printed:
// hand the bytes to the dispatcher, wait for the printer's answer, report.
// Only a print the printer confirmed goes on the wall.
func (a *api) send(w http.ResponseWriter, r *http.Request, payload []byte, p Print, attrs ...any) {
	// The trace id is the job id, so one grep follows a receipt from here to
	// the firmware. Stored nowhere: it lives as long as the request.
	id := logging.Trace(r.Context())

	// Blocks until the firmware has answered for the job: about a second when
	// the printer is there, and never past timeouts.Print when it is not.
	ctx, cancel := context.WithTimeout(r.Context(), timeouts.Print)
	defer cancel()

	if err := a.dispatcher.Print(ctx, id, payload); err != nil {
		a.upstreamFailed(w, r, "dispatcher", err)
		return
	}

	a.log.InfoContext(r.Context(), "printed", append([]any{"bytes", len(payload)}, attrs...)...)

	p.ID, p.Time = id, time.Now().UTC()
	if err := a.wall.Add(p); err != nil {
		a.log.ErrorContext(r.Context(), "cannot put print on the wall", "err", err)
	}

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

// A page of the wall is capped: a whole year in one reply would be megabytes.
const (
	defaultPage = 200
	maxPage     = 1000
)

// prints pages through the wall, newest first: ?before=<id>&limit=<n>.
func (a *api) prints(w http.ResponseWriter, r *http.Request) {
	limit := defaultPage
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxPage {
			a.fail(w, r, http.StatusBadRequest, fmt.Sprintf("limit must be 1 to %d", maxPage))
			return
		}
		limit = n
	}

	page, more, ok := a.wall.Page(r.URL.Query().Get("before"), limit)
	if !ok {
		a.fail(w, r, http.StatusBadRequest, "no print with that id")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"prints": page, "more": more})
}

// streamPing keeps a quiet stream alive through proxies that close idle
// connections.
const streamPing = 25 * time.Second

// stream sends every new print, and every hidden one, as server-sent events
// for as long as the viewer stays.
func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	// The server's read and write timeouts are for requests that end; this
	// one does not, and would otherwise be cut off at WriteTimeout.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "cannot stream")
		return
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "cannot stream")
		return
	}

	events, ok := a.wall.Watch()
	if !ok {
		a.fail(w, r, http.StatusServiceUnavailable, "too many viewers, try again later")
		return
	}
	defer a.wall.Unwatch(events)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if rc.Flush() != nil {
		return
	}

	ping := time.NewTicker(streamPing)
	defer ping.Stop()

	for {
		var err error

		select {
		case <-r.Context().Done():
			return
		case ev, open := <-events:
			if !open {
				return
			}
			_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
		case <-ping.C:
			_, err = fmt.Fprint(w, ": ping\n\n")
		}

		if err != nil || rc.Flush() != nil {
			return
		}
	}
}

// photo serves the picture kept for a photo print on the wall.
func (a *api) photo(w http.ResponseWriter, r *http.Request) {
	p, ok := a.wall.Get(r.PathValue("id"))
	if !ok || p.Kind != "photo" || a.photos == "" {
		httpx.WriteError(w, http.StatusNotFound, "no such photo")
		return
	}

	// A stranger's upload, checked only as far as its PNG header: never let
	// a browser read it as anything but an image.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, filepath.Join(a.photos, p.ID+".png"))
}

func (a *api) hide(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	found, err := a.wall.Hide(id)
	switch {
	case err != nil:
		a.log.ErrorContext(r.Context(), "cannot hide print", "print", id, "err", err)
		a.fail(w, r, http.StatusInternalServerError, "cannot hide print")
	case !found:
		a.fail(w, r, http.StatusNotFound, "no print with that id")
	default:
		a.log.InfoContext(r.Context(), "hid print", "print", id)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"id": id, "status": "hidden"})
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
