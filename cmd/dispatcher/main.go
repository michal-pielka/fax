// Command dispatcher owns the MQTT connection to the printer.
//
// It is the only process allowed to publish jobs, and must run as a single
// instance: two of them would each publish every job, and the printer would
// produce two receipts.
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
)

const connectTimeout = 15 * time.Second

func main() {
	addr := flag.String("addr", ":8082", "listen address")
	broker := flag.String("broker", "tcp://localhost:1883",
		"broker URL; use ssl://host:8883 for anything crossing the internet")
	clientID := flag.String("client-id", "fax-dispatcher", "MQTT client id")
	username := flag.String("username", "backend", "MQTT username")
	device := flag.String("device", "printer-1", "device id, used to build topic names")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// The password comes from the environment, not a flag: flags are visible
	// in ps output and shell history to every user on the box.
	password := os.Getenv("MQTT_PASSWORD")
	if password == "" {
		log.Warn("MQTT_PASSWORD is empty; this only works on a broker that allows anonymous access")
	}

	dev := NewDevice(Config{
		Broker:   *broker,
		ClientID: *clientID,
		Username: *username,
		Password: password,
		Device:   *device,
	}, log)

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), connectTimeout)
	defer cancelConnect()

	// Failing to connect at startup is fatal rather than retried in the
	// background: a dispatcher that cannot reach its broker has no purpose,
	// and dying loudly beats silently accepting jobs that go nowhere.
	if err := dev.Connect(connectCtx); err != nil {
		log.Error("cannot connect to broker", "broker", *broker, "err", err)
		os.Exit(1)
	}
	defer dev.Close()

	a := &api{printer: dev, log: log}

	srv := &http.Server{
		Addr:    *addr,
		Handler: a.routes(),
		// Longer write timeout than the renderer's: publishing waits on a
		// broker acknowledgement, which crosses the network.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", srv.Addr, "broker", *broker, "device", *device)
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
