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
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/logging"
)

// The two upstreams have nothing in common, so they do not share a timeout.
// Rendering is CPU work measured in milliseconds. Printing is a physical act
// that ends when paper stops moving, which for a raster image at 9600 baud is
// tens of seconds. One shared value would either cut long prints short or let
// a hung renderer hold a connection open for just as long.
//
// Both have to leave room under the server's WriteTimeout below.
const (
	renderTimeout = 5 * time.Second
	printTimeout  = 35 * time.Second
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	rendererURL := flag.String("renderer", "http://localhost:8081", "renderer service base URL")
	dispatcherURL := flag.String("dispatcher", "http://localhost:8082", "dispatcher service base URL")
	maxRunes := flag.Int("max-runes", 255, "longest document accepted; must match the renderer")
	logFormat := flag.String("log-format", "json", "log format: json or text")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")
	flag.Parse()

	log, err := logging.New(*logFormat, *logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// A client each, because the timeouts differ. Both still pool their own
	// connections, and the link between containers is never the slow part.
	dispatcher := NewDispatcherClient(*dispatcherURL, &http.Client{Timeout: printTimeout})

	a := &api{
		renderer:   NewRendererClient(*rendererURL, &http.Client{Timeout: renderTimeout}),
		dispatcher: dispatcher,
		hub:        newHub(dispatcher, log),
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
		// Must exceed printTimeout. WriteTimeout covers everything from the
		// end of the request headers to the last byte of the response, and a
		// print request holds that open for as long as the paper moves.
		WriteTimeout: 40 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Ctrl-C and SIGTERM stop accepting new connections and let in-flight
	// requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One connection to the dispatcher, held for the life of the process and
	// reconnected when it drops. Started before the listener so the first
	// browser to arrive already has something to be told.
	go a.hub.run(ctx)

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
