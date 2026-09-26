// Command gateway is the public edge: the only process reachable from the
// internet. It validates, calls the renderer then the dispatcher, holds no state.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/httpx"
	"github.com/michal-pielka/fax/server/internal/logging"
)

// Rendering is quick, so its client has a flat timeout. Printing waits on
// the wire and gets a per-request deadline instead (see printTimeout).
const renderTimeout = 5 * time.Second

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	rendererURL := flag.String("renderer", "http://localhost:8081", "renderer service base URL")
	dispatcherURL := flag.String("dispatcher", "http://localhost:8082", "dispatcher service base URL")
	photosDir := flag.String("photos-dir", "", "keep a copy of every printed photo here; empty keeps none")
	logFlags := logging.RegisterFlags()
	flag.Parse()

	log, err := logFlags.Logger()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if *photosDir != "" {
		if err := os.MkdirAll(*photosDir, 0o755); err != nil {
			log.Error("cannot create photos dir", "path", *photosDir, "err", err)
			os.Exit(1)
		}
	}

	// A client each, because the timeouts differ. The dispatcher's has none
	// of its own: every print carries a deadline sized to its payload.
	a := &api{
		renderer:   NewRendererClient(*rendererURL, &http.Client{Timeout: renderTimeout}),
		dispatcher: NewDispatcherClient(*dispatcherURL, &http.Client{}),
		limits:     doc.Paper,
		photos:     *photosDir,
		log:        log,
	}

	srv := &http.Server{
		Addr: *addr,
		// Outermost, so a request that never reaches a handler is still logged.
		Handler: logging.Requests(log)(a.routes()),
		// Without these one slow client holds a connection open forever.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Must exceed the longest print: a square photo may wait close to
		// fifty seconds (see printTimeout).
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := httpx.Serve(srv, log, "renderer", *rendererURL, "dispatcher", *dispatcherURL); err != nil {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
