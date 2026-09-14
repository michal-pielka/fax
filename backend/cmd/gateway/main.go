// Command gateway is the public edge: the only process reachable from the
// internet. It validates, calls the renderer then the dispatcher, holds no state.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
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
	logFormat := flag.String("log-format", "json", "log format: json or text")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")
	flag.Parse()

	log, err := logging.New(*logFormat, *logLevel)
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
		// Must exceed the longest print: a square photo is about 30 seconds
		// of wire plus the dispatcher's margin (see printTimeout).
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Stop accepting, then let in-flight requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", srv.Addr, "renderer", *rendererURL, "dispatcher", *dispatcherURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
}
