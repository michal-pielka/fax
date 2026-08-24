// Command renderer is the service that turns documents into printer bytes.
//
// It is a thin shell around internal/render: decode, validate, render, encode.
// All the logic lives in that package, which is why the package has thorough
// tests and this command has few.
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

// maxRunes has to match the gateway's limit. The renderer repeats the check
// rather than trusting it: this is a separate process, and Render's assumption
// that byte offsets equal character positions only holds for validated text.
const maxRunes = 255

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	a := &api{
		limits: doc.Limits{MaxRunes: maxRunes},
		log:    log,
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: a.routes(),
		// Shorter than the gateway's: rendering is pure computation with no
		// hardware to wait on, so nothing here should take seconds.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Ctrl-C and SIGTERM stop accepting new connections and let in-flight
	// requests finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", srv.Addr)
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
