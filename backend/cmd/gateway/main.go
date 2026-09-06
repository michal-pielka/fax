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

// Rendering is quick. Printing blocks until the firmware answers, so its
// timeout must exceed the dispatcher's ackTimeout: a request cut off early
// reads as a dead dispatcher when the truth was a slow printer.
const (
	renderTimeout = 5 * time.Second
	printTimeout  = 10 * time.Second
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	rendererURL := flag.String("renderer", "http://localhost:8081", "renderer service base URL")
	dispatcherURL := flag.String("dispatcher", "http://localhost:8082", "dispatcher service base URL")
	// 9 rows of 32 columns plus the 8 newlines between them: one full page.
	maxRunes := flag.Int("max-runes", 296, "longest document accepted; must match the renderer")
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
	a := &api{
		renderer:   NewRendererClient(*rendererURL, &http.Client{Timeout: renderTimeout}),
		dispatcher: NewDispatcherClient(*dispatcherURL, &http.Client{Timeout: printTimeout}),
		limits:     doc.Limits{MaxRunes: *maxRunes},
		log:        log,
	}

	srv := &http.Server{
		Addr: *addr,
		// Outermost, so a request that never reaches a handler is still logged.
		Handler: logging.Requests(log)(a.routes()),
		// Without these one slow client holds a connection open forever.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Must exceed renderTimeout plus printTimeout, the longest a print
		// request can legitimately take.
		WriteTimeout: 20 * time.Second,
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
