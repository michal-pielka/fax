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
)

// Errors the printer itself is responsible for, as opposed to the dispatcher
// failing. Handlers turn these into statuses the caller can act on.
var (
	ErrOffline = errors.New("printer is offline")
	ErrNoPaper = errors.New("printer is out of paper")
	ErrBusy    = errors.New("printer is busy")
)

// ackTimeout must exceed the firmware's PRINT_TIMEOUT, so a printer that gives
// up gets to say why rather than leaving this to guess.
const ackTimeout = 32 * time.Second

// State is what the device last told us. The zero value is offline with no
// paper: until it says otherwise, assume nothing can be printed.
type State struct {
	Online bool `json:"online"`
	Paper  bool `json:"paper"`
}

// Printer is the half of Device the handlers use, so they can be tested
// without a broker.
type Printer interface {
	Publish(ctx context.Context, id string, payload []byte) error
	State() State
	Busy() bool
	LastJob() (Result, bool)
	Subscribe() (<-chan struct{}, func())
}

// Device owns the MQTT connection. The process must be a singleton: two
// dispatchers would both publish, and every receipt would print twice.
type Device struct {
	client mqtt.Client
	topics topics
	log    *slog.Logger

	mu    sync.RWMutex
	state State
	// How the last job ended, nil until one has. Under mu, since it is read
	// in the same breath as state.
	last *Result

	// One in-flight job at a time: a second would interleave with the first.
	// Its own mutex, since this is touched on every ack.
	waitMu  sync.Mutex
	waiting map[string]chan ack

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// ack is what the firmware says about one job. The reason is a code rather
// than a sentence so callers switch on it instead of matching strings.
type ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// reasonNoConfirmation is the firmware's code for a printer that never came
// back, and is also what this records when the firmware itself says nothing.
const reasonNoConfirmation = "no_confirmation"

// Result is how one job ended, as it reaches the browser.
type Result struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

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
	Broker   string
	ClientID string
	Username string
	Password string
	Device   string
	// The URL scheme decides TLS: ssl://host:8883 in production, tcp:// only
	// where credentials crossing in the clear is acceptable.
}

func NewDevice(cfg Config, log *slog.Logger) *Device {
	d := &Device{
		topics:  newTopics(cfg.Device),
		log:     log,
		waiting: make(map[string]chan ack),
		subs:    make(map[chan struct{}]struct{}),
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

	d.log.Info("device state", "online", s.Online, "paper", s.Paper)
	d.setState(s)
}

// Subscribe fires whenever anything observable changes. It signals *that*,
// not what, which keeps notify out of both mutexes and out of deadlock.
func (d *Device) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	d.subMu.Lock()
	d.subs[ch] = struct{}{}
	d.subMu.Unlock()

	return ch, func() {
		d.subMu.Lock()
		delete(d.subs, ch)
		d.subMu.Unlock()
	}
}

// notify must never block: onState runs on paho's single message goroutine.
// Capacity one also coalesces a burst into one wake-up.
func (d *Device) notify() {
	d.subMu.Lock()
	defer d.subMu.Unlock()

	for ch := range d.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
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

	d.notify()
}

func (d *Device) State() State {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.state
}

// Publish returns as soon as the broker has the job. What is knowable up front
// is an error here; how it ended arrives later as a state event.
func (d *Device) Publish(ctx context.Context, id string, payload []byte) error {
	st := d.State()

	switch {
	case !st.Online:
		return ErrOffline
	case !st.Paper:
		return ErrNoPaper
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

	// Not tied to ctx: the paper is moving regardless, and abandoning this
	// would leave the printer claimed forever.
	go d.awaitAck(id, acks)

	return nil
}

// awaitAck records how a job ended and frees the printer. setResult does not
// notify, so the deferred stopWaiting emits both changes as one event.
func (d *Device) awaitAck(id string, acks <-chan ack) {
	defer d.stopWaiting(id)

	select {
	case a := <-acks:
		d.setResult(Result{ID: id, OK: a.OK, Error: a.Error})

	case <-time.After(ackTimeout):
		// The receipt may well be in the printer right now. All that is
		// certain is that nobody said so.
		d.log.Warn("no acknowledgement", "trace", id)
		d.setResult(Result{ID: id, Error: reasonNoConfirmation})
	}
}

func (d *Device) setResult(r Result) {
	d.mu.Lock()
	d.last = &r
	d.mu.Unlock()
}

// LastJob reports how the most recent job ended. One slot is enough: only one
// runs at a time, so it is the only one anybody can still be asking about.
func (d *Device) LastJob() (Result, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.last == nil {
		return Result{}, false
	}

	return *d.last, true
}

// wait claims the printer for one job. One slot, because there is one printer
// and no queue to put a second job in.
func (d *Device) wait(id string) (<-chan ack, error) {
	d.waitMu.Lock()

	if len(d.waiting) > 0 {
		d.waitMu.Unlock()
		return nil, ErrBusy
	}

	// Buffered, so onAck never blocks on a waiter that has already timed out.
	ch := make(chan ack, 1)
	d.waiting[id] = ch
	d.waitMu.Unlock()

	// After the unlock: notify takes subMu, and never overlapping the two
	// makes the lock ordering unarguable.
	d.notify()

	return ch, nil
}

func (d *Device) stopWaiting(id string) {
	d.waitMu.Lock()
	delete(d.waiting, id)
	d.waitMu.Unlock()

	d.notify()
}

// Busy needs no bookkeeping: an unfinished job is exactly an outstanding
// waiter.
func (d *Device) Busy() bool {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()

	return len(d.waiting) > 0
}
