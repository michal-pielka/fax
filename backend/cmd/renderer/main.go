// Command renderer turns documents into printer bytes: a thin shell around
// internal/render, which is where the logic and the thorough tests live.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/michal-pielka/fax/server/internal/httpx"
	"github.com/michal-pielka/fax/server/internal/logging"
)

func main() {
	addr := flag.String("addr", ":8081", "listen address")
	logFlags := logging.RegisterFlags()
	flag.Parse()

	log, err := logFlags.Logger()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	a := &api{log: log}

	srv := &http.Server{
		Addr: *addr,
		// Outermost, so a request that never reaches a handler is still logged.
		Handler: logging.Requests(log)(a.routes()),
		// Shorter than the gateway's: rendering is pure computation with no
		// hardware to wait on, so nothing here should take seconds.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if err := httpx.Serve(srv, log); err != nil {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
