package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/michal-pielka/fax/server/internal/logging"
	"github.com/michal-pielka/fax/server/internal/wire"
)

// Errors the printer itself is responsible for, as opposed to the dispatcher
// failing. Handlers turn these into statuses the caller can act on.
var (
	ErrOffline = errors.New("printer is offline")
	ErrNoPaper = errors.New("printer is out of paper")
	ErrBusy    = errors.New("printer is busy")
	// ErrNoConfirmation is the honest answer for a printer that took the job
	// and said nothing: it may well have printed, and nobody can say so.
	ErrNoConfirmation = errors.New("printer did not confirm; it may still have printed")
)

// ackTimeout is how long to wait for the firmware's answer to a job of n
// bytes. The bytes take their wire time to reach the printer; the firmware
// then allows the printer the same again to finish printing them, since a
// dense photo prints slower than it arrives, plus five seconds; two more here
// so a printer that gives up gets to say why rather than leaving this to
// guess. The gateway's deadline must exceed this in turn.
func ackTimeout(n int) time.Duration {
	return 2*wire.Time(n) + 7*time.Second
}

// State is what the device last told us: the retained message it publishes on
// connect, or the last will the broker publishes when it vanishes. The zero
// value is offline, so until it says otherwise nothing can be printed. Paper is
// not here on purpose: the firmware measures it per job and answers in the ack.
type State struct {
	Online bool `json:"online"`
}

// Printer is the half of Device the handlers use, so they can be tested
// without a broker.
type Printer interface {
	Publish(ctx context.Context, id string, payload []byte) error
	State() State
}

// Device owns the MQTT connection. The process must be a singleton: two
// dispatchers would both publish, and every receipt would print twice.
type Device struct {
	client mqtt.Client
	topics topics
	log    *slog.Logger

	mu    sync.RWMutex
	state State

	// One in-flight job at a time: a second would interleave with the first.
	// Its own mutex, since this is touched on every ack.
	waitMu  sync.Mutex
	waiting map[string]chan ack
}

// ack is what the firmware says about one job. The reason is a code rather
// than a sentence so callers switch on it instead of matching strings.
type ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// The firmware's reason codes. Anything else is reported verbatim.
const (
	reasonNoPaper        = "no_paper"
	reasonNoConfirmation = "no_confirmation"
)

// topics are derived from the device id, so a second printer needs
// configuration rather than code.
type topics struct {
	// A prefix; the id is appended. In the topic rather than the payload, so
	// the payload stays raw ESC/POS with no parser on the device.
	job string
	// Retained, and carries the last will, so subscribing yields the current
	// state even after a restart.
	state string
	// A prefix mirroring job. Never retained: a retained ack would replay on
	// every reconnect, long after anyone was waiting.
	ack string
}

func newTopics(device string) topics {
	return topics{
		job:   fmt.Sprintf("fax/%s/job", device),
		state: fmt.Sprintf("fax/%s/state", device),
		ack:   fmt.Sprintf("fax/%s/ack", device),
	}
}

type Config struct {
	// The URL scheme decides TLS: ssl://host:8883 in production, tcp:// only
	// where credentials crossing in the clear is acceptable.
	Broker   string
	ClientID string
	Username string
	Password string
	Device   string
}

func NewDevice(cfg Config, log *slog.Logger) *Device {
	d := &Device{
		topics:  newTopics(cfg.Device),
		log:     log,
		waiting: make(map[string]chan ack),
	}

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

	// QoS 1: a lost state change leaves us lying for hours. Blocking is safe
	// here -- paho runs OnConnect on its own goroutine, unlike the handlers.
	for topic, handler := range map[string]mqtt.MessageHandler{
		d.topics.state:      d.onState,
		d.topics.ack + "/+": d.onAck,
	} {
		tok := c.Subscribe(topic, 1, handler)
		if tok.Wait() && tok.Error() != nil {
			d.log.Error("subscribe failed", "topic", topic, "err", tok.Error())
		}
	}
}

// onConnectionLost clears the state: the device may be fine, but claiming so
// having lost the only channel that would say otherwise is worse.
func (d *Device) onConnectionLost(_ mqtt.Client, err error) {
	d.log.Warn("broker connection lost", "err", err)
	d.setState(State{})
}

func (d *Device) onState(_ mqtt.Client, m mqtt.Message) {
	var s State
	if err := json.Unmarshal(m.Payload(), &s); err != nil {
		// Truncated: this is whatever the device sent, and it should not be
		// able to decide how much of your log it occupies.
		d.log.Error("bad state payload", "payload", logging.Truncate(string(m.Payload()), 120), "err", err)
		return
	}

	d.log.Info("device state", "online", s.Online)
	d.setState(s)
}

// onAck must never block: paho's Order defaults to true, so handlers share one
// goroutine and the PUBACK waits on this returning.
func (d *Device) onAck(_ mqtt.Client, m mqtt.Message) {
	// The id is the last segment of fax/<device>/ack/<id>.
	parts := strings.Split(m.Topic(), "/")
	id := parts[len(parts)-1]

	var a ack
	if err := json.Unmarshal(m.Payload(), &a); err != nil {
		d.log.Error("bad ack payload", "trace", id, "payload", logging.Truncate(string(m.Payload()), 120), "err", err)
		return
	}

	d.log.Info("device ack", "trace", id, "ok", a.OK, "reason", a.Error)

	d.waitMu.Lock()
	ch, ok := d.waiting[id]
	d.waitMu.Unlock()

	if !ok {
		// The request gave up, or this is a duplicate delivery of an ack we
		// already handled. Either way there is nobody left to tell.
		d.log.Warn("ack for an unknown job", "trace", id)
		return
	}

	// Buffered, and drained by exactly one waiter, so this cannot block --
	// but the select is what guarantees it even if that stops being true.
	select {
	case ch <- a:
	default:
	}
}

func (d *Device) setState(s State) {
	d.mu.Lock()
	d.state = s
	d.mu.Unlock()
}

func (d *Device) State() State {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.state
}

// Publish sends one job and returns once the firmware has answered for it.
// nil means the printer took the bytes with paper in; the errors above say
// what stopped it. The printer is claimed for the whole exchange.
func (d *Device) Publish(ctx context.Context, id string, payload []byte) error {
	// The one check made here: an offline device would cost the caller the
	// full ackTimeout to learn what the last will already says.
	if !d.State().Online {
		return ErrOffline
	}

	// Before the publish, not after: the printer answers in milliseconds, and
	// an ack without a waiter is an ack nobody hears.
	acks, err := d.wait(id)
	if err != nil {
		return err
	}

	topic := d.topics.job + "/" + id

	// retained must stay false: a retained job reprints on every reconnect.
	tok := d.client.Publish(topic, 1, false, payload)

	select {
	case <-tok.Done():
		if err := tok.Error(); err != nil {
			d.stopWaiting(id)

			return fmt.Errorf("publish to %s: %w", topic, err)
		}

	case <-ctx.Done():
		d.stopWaiting(id)

		return ctx.Err()
	}

	d.log.InfoContext(ctx, "published", "bytes", len(payload))

	// The wait runs on its own goroutine, not tied to ctx: the paper is moving
	// regardless, and a caller that gives up must not release the printer
	// while the job is still on it.
	done := make(chan error, 1)

	go func() { done <- d.awaitAck(id, acks, ackTimeout(len(payload))) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitAck turns the firmware's answer into an error, and frees the printer
// whatever the answer was.
func (d *Device) awaitAck(id string, acks <-chan ack, timeout time.Duration) error {
	defer d.stopWaiting(id)

	select {
	case a := <-acks:
		switch {
		case a.OK:
			return nil
		case a.Error == reasonNoPaper:
			// Measured by the firmware right before it would have printed.
			return ErrNoPaper
		case a.Error == reasonNoConfirmation:
			return ErrNoConfirmation
		default:
			return fmt.Errorf("printer refused: %s", a.Error)
		}

	case <-time.After(timeout):
		// The receipt may well be in the printer right now. All that is
		// certain is that nobody said so.
		d.log.Warn("no acknowledgement", "trace", id)

		return ErrNoConfirmation
	}
}

// wait claims the printer for one job. One slot, because there is one printer
// and no queue to put a second job in.
func (d *Device) wait(id string) (<-chan ack, error) {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()

	if len(d.waiting) > 0 {
		return nil, ErrBusy
	}

	// Buffered, so onAck never blocks on a waiter that has already timed out.
	ch := make(chan ack, 1)
	d.waiting[id] = ch

	return ch, nil
}

func (d *Device) stopWaiting(id string) {
	d.waitMu.Lock()
	delete(d.waiting, id)
	d.waitMu.Unlock()
}
