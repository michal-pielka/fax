// Command gateway is the public edge of the service.
//
// It is the only process reachable from the internet: it validates what
// arrives, calls the renderer and the dispatcher in turn, and reports what
// happened. It holds no state -- printing is synchronous, so a job lives
// exactly as long as the request that carried it.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// upstreamTimeout bounds a call to the renderer or the dispatcher, and has to
// leave room under the server's own WriteTimeout below.
const upstreamTimeout = 12 * time.Second

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	rendererURL := flag.String("renderer", "http://localhost:8081", "renderer service base URL")
	dispatcherURL := flag.String("dispatcher", "http://localhost:8082", "dispatcher service base URL")
	maxRunes := flag.Int("max-runes", 255, "longest document accepted; must match the renderer")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// One client, shared: it pools connections, and both upstreams sit on the
	// same host over a link that is never the slow part.
	hc := &http.Client{Timeout: upstreamTimeout}

	a := &api{
		renderer:   NewRendererClient(*rendererURL, hc),
		dispatcher: NewDispatcherClient(*dispatcherURL, hc),
		limits:     doc.Limits{MaxRunes: *maxRunes},
		log:        log,
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: a.routes(),
		// A public service needs these. Without them one slow client can
		// hold a connection open indefinitely.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Ctrl-C and SIGTERM stop accepting new connections and let in-flight
	// requests finish.
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
