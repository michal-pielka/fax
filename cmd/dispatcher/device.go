package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Errors the printer itself is responsible for, as opposed to the dispatcher
// failing. Handlers turn these into statuses the caller can act on.
var (
	ErrOffline = errors.New("printer is offline")
	ErrNoPaper = errors.New("printer is out of paper")
)

// State is what the device last told us about itself.
//
// The zero value is "offline, no paper", which is the right default: until the
// device says otherwise, assume nothing can be printed.
type State struct {
	Online bool `json:"online"`
	Paper  bool `json:"paper"`
}

// Printer is the half of Device the handlers use, so they can be tested
// without a broker.
type Printer interface {
	Publish(ctx context.Context, id string, payload []byte) error
	State() State
}

// Device owns the MQTT connection and everything known about the printer.
//
// There is exactly one of these per process, and the process must be a
// singleton: two dispatchers would both publish, and the printer would run
// every job twice.
type Device struct {
	client mqtt.Client
	topics topics
	log    *slog.Logger

	mu    sync.RWMutex
	state State
}

// topics groups the two the dispatcher cares about. They are derived from the
// device id so a second printer needs configuration, not code.
type topics struct {
	// job is a prefix: the id is appended, making the full topic
	// fax/<device>/job/<id>.
	//
	// Putting the id in the topic rather than the payload keeps the payload
	// raw ESC/POS, so the firmware writes it straight to the UART with no JSON
	// parser and no base64 decoder on a device with 300KB of RAM.
	job string
	// state is retained and carries the device's last will, so subscribing
	// yields the current state immediately even after a dispatcher restart.
	state string
}

func newTopics(device string) topics {
	return topics{
		job:   fmt.Sprintf("fax/%s/job", device),
		state: fmt.Sprintf("fax/%s/state", device),
	}
}

type Config struct {
	Broker   string
	ClientID string
	Username string
	Password string
	Device   string
	// TLS is not configured here: the broker URL scheme decides it. Use
	// ssl://host:8883 in production and tcp://host:1883 only on a trusted
	// network -- credentials cross the wire in the clear otherwise.
}

func NewDevice(cfg Config, log *slog.Logger) *Device {
	d := &Device{topics: newTopics(cfg.Device), log: log}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		// The dispatcher runs for months; brokers restart and networks drop.
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetConnectRetry(true).
		// A clean session: the dispatcher has nothing worth resuming, and
		// stale subscriptions would only confuse a restart.
		SetCleanSession(true).
		SetKeepAlive(30 * time.Second)

	// Subscriptions do not survive a reconnect, so they are re-established
	// every time the connection comes up rather than once at startup.
	opts.SetOnConnectHandler(d.onConnect)
	opts.SetConnectionLostHandler(d.onConnectionLost)

	d.client = mqtt.NewClient(opts)

	return d
}

// Connect blocks until the broker accepts the connection or ctx expires.
func (d *Device) Connect(ctx context.Context) error {
	tok := d.client.Connect()

	select {
	case <-tok.Done():
		return tok.Error()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Device) Close() {
	d.client.Disconnect(250)
}

func (d *Device) onConnect(c mqtt.Client) {
	d.log.Info("connected to broker")

	// QoS 1: losing a state change would leave the dispatcher lying about the
	// printer indefinitely, since the next one may be hours away.
	tok := c.Subscribe(d.topics.state, 1, d.onState)
	if tok.Wait() && tok.Error() != nil {
		d.log.Error("subscribe failed", "topic", d.topics.state, "err", tok.Error())
	}
}

// onConnectionLost clears the state. The device may well be fine, but we can
// no longer know that, and claiming a printer is online when we have lost the
// only channel that would tell us otherwise is worse than admitting ignorance.
func (d *Device) onConnectionLost(_ mqtt.Client, err error) {
	d.log.Warn("broker connection lost", "err", err)
	d.setState(State{})
}

func (d *Device) onState(_ mqtt.Client, m mqtt.Message) {
	var s State
	if err := json.Unmarshal(m.Payload(), &s); err != nil {
		d.log.Error("bad state payload", "payload", string(m.Payload()), "err", err)
		return
	}

	d.log.Info("device state", "online", s.Online, "paper", s.Paper)
	d.setState(s)
}

func (d *Device) setState(s State) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = s
}

func (d *Device) State() State {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.state
}

// Publish sends one job to the printer.
//
// It reports only that the broker accepted the message, not that anything was
// printed: the firmware does not acknowledge jobs yet. Once it does, this is
// where waiting for that acknowledgement belongs.
func (d *Device) Publish(ctx context.Context, id string, payload []byte) error {
	st := d.State()

	switch {
	case !st.Online:
		return ErrOffline
	case !st.Paper:
		return ErrNoPaper
	}

	topic := d.topics.job + "/" + id

	// retained is false, and must stay false. A retained job would be
	// redelivered every time the ESP32 reconnects, so one receipt would print
	// again on every power cycle for as long as it sat on the broker.
	tok := d.client.Publish(topic, 1, false, payload)

	select {
	case <-tok.Done():
		if err := tok.Error(); err != nil {
			return fmt.Errorf("publish to %s: %w", topic, err)
		}

		d.log.Info("published", "id", id, "bytes", len(payload))

		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
