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

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	a := &api{
		store:  NewStore(),
		limits: doc.Limits{MaxRunes: 255},
		log:    log,
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
