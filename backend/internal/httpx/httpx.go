// Package httpx is what the three services' HTTP layers share: JSON replies
// and running a server until it is told to stop.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// shutdownGrace is how long in-flight requests get once a stop is asked for.
const shutdownGrace = 10 * time.Second

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg})
}

// Health answers for the process alone. Nothing downstream is consulted, so
// an offline printer or a slow renderer never gets a healthy service restarted.
func Health(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Serve runs srv until SIGINT or SIGTERM, then stops accepting and lets
// in-flight requests finish. It returns only a failure to listen or to shut
// down cleanly; attrs are added to the "listening" line.
func Serve(srv *http.Server, log *slog.Logger, attrs ...any) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	failed := make(chan error, 1)

	go func() {
		log.Info("listening", append([]any{"addr", srv.Addr}, attrs...)...)

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}
