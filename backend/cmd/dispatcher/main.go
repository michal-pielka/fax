// Command dispatcher owns the MQTT connection and is the only process allowed
// to publish jobs. Two instances would print every receipt twice.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/michal-pielka/fax/server/internal/httpx"
	"github.com/michal-pielka/fax/server/internal/logging"
)

const connectTimeout = 15 * time.Second

func main() {
	addr := flag.String("addr", ":8082", "listen address")
	broker := flag.String("broker", "tcp://localhost:1883",
		"broker URL; use ssl://host:8883 for anything crossing the internet")
	clientID := flag.String("client-id", "fax-dispatcher", "MQTT client id")
	username := flag.String("username", "backend", "MQTT username")
	device := flag.String("device", "printer-1", "device id, used to build topic names")
	logFlags := logging.RegisterFlags()
	flag.Parse()

	log, err := logFlags.Logger()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

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

	// Fatal rather than retried: dying loudly beats silently accepting jobs
	// that go nowhere.
	if err := dev.Connect(connectCtx); err != nil {
		log.Error("cannot connect to broker", "broker", *broker, "err", err)
		os.Exit(1)
	}

	a := &api{printer: dev, log: log}

	srv := &http.Server{
		Addr: *addr,
		// Reuses the trace id the gateway sent, so one request reads as one
		// trace across both services rather than two unrelated ones.
		Handler:           logging.Requests(log)(a.routes()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Must exceed the longest ackTimeout, or a print request that is
		// legitimately waiting on a photo is cut off before it can answer.
		// A square photo may wait about 45 seconds.
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	err = httpx.Serve(srv, log, "broker", *broker, "device", *device)
	dev.Close()

	if err != nil {
		log.Error("server failed", "err", err)
		os.Exit(1)
	}
}
