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
)

// Errors the printer itself is responsible for, as opposed to the dispatcher
// failing. Handlers turn these into statuses the caller can act on.
var (
	ErrOffline = errors.New("printer is offline")
	ErrNoPaper = errors.New("printer is out of paper")
	ErrBusy    = errors.New("printer is busy")
)

// ackTimeout bounds the wait for the firmware to report a job finished. It has
// to exceed the firmware's own PRINT_TIMEOUT, so that a printer which gives up
// gets to say why instead of leaving us guessing.
const ackTimeout = 32 * time.Second

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
	Busy() bool
	LastJob() (Result, bool)
	Subscribe() (<-chan struct{}, func())
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
	// last is how the most recent job ended, nil until one has. Guarded by mu
	// because it is read in the same breath as state.
	last *Result

	// One in-flight job at a time, keyed by id. The printer is a single
	// physical thing, so a second job would interleave with the first --
	// which is why a busy printer refuses work rather than queueing it.
	//
	// Its own mutex, not mu: this is touched on every ack, and mu is read on
	// every request.
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
	// ack is a prefix, mirroring job: the firmware replies on
	// fax/<device>/ack/<id> once the paper has actually moved. Never
	// retained -- a retained ack would be redelivered on every reconnect,
	// long after the request that cared about it had gone.
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
	// TLS is not configured here: the broker URL scheme decides it. Use
	// ssl://host:8883 in production and tcp://host:1883 only on a trusted
	// network -- credentials cross the wire in the clear otherwise.
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

	// QoS 1: losing a state change would leave the dispatcher lying about the
	// printer indefinitely, since the next one may be hours away.
	//
	// Blocking here is safe. paho calls this handler on its own goroutine
	// (`go c.options.OnConnect(c)`), unlike the message handlers below.
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
		// Truncated: this is whatever the device sent, and it should not be
		// able to decide how much of your log it occupies.
		d.log.Error("bad state payload", "payload", truncate(string(m.Payload()), 120), "err", err)
		return
	}

	d.log.Info("device state", "online", s.Online, "paper", s.Paper)
	d.setState(s)
}

// Subscribe returns a channel that fires whenever anything observable about
// the printer changes, plus the function that stops it.
//
// It signals *that* something changed rather than what. Subscribers then read
// State and Busy themselves, which keeps notify out of both mutexes -- they
// are taken in different orders on different paths, so a notify that built a
// snapshot would be a deadlock waiting to happen.
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

// notify wakes every subscriber.
//
// Nothing here may block: this is reached from onState, which paho runs on the
// one goroutine it uses for every inbound message. Capacity one plus a
// non-blocking send also means a burst of changes coalesces into a single
// wake-up, and the subscriber reads the final state rather than replaying
// three intermediate ones that are already wrong.
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

// onAck matches a reply to the request waiting for it.
//
// Nothing in here may block. paho's Order option defaults to true, so every
// message handler runs in turn on one shared goroutine, and the PUBACK is only
// sent once this returns -- a stall here freezes the whole connection.
func (d *Device) onAck(_ mqtt.Client, m mqtt.Message) {
	// The id is the last segment of fax/<device>/ack/<id>.
	parts := strings.Split(m.Topic(), "/")
	id := parts[len(parts)-1]

	var a ack
	if err := json.Unmarshal(m.Payload(), &a); err != nil {
		d.log.Error("bad ack payload", "id", id, "payload", truncate(string(m.Payload()), 120), "err", err)
		return
	}

	d.log.Info("device ack", "id", id, "ok", a.OK, "reason", a.Error)

	d.waitMu.Lock()
	ch, ok := d.waiting[id]
	d.waitMu.Unlock()

	if !ok {
		// The request gave up, or this is a duplicate delivery of an ack we
		// already handled. Either way there is nobody left to tell.
		d.log.Warn("ack for an unknown job", "id", id)
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

// Publish sends one job to the printer.
//
// It returns as soon as the broker has the job, not when the paper stops
// moving. Everything knowable up front is still an error here -- no printer,
// no paper, already printing -- and how the job actually ended arrives later
// as a state event, because holding an HTTP request open for thirty seconds
// is a fragile way to deliver one bit that is already being broadcast.
func (d *Device) Publish(ctx context.Context, id string, payload []byte) error {
	st := d.State()

	switch {
	case !st.Online:
		return ErrOffline
	case !st.Paper:
		return ErrNoPaper
	}

	// Registered before the publish, not after: the printer can answer in
	// milliseconds, and an ack that arrives before its waiter exists is an ack
	// nobody hears.
	acks, err := d.wait(id)
	if err != nil {
		return err
	}

	topic := d.topics.job + "/" + id

	// retained is false, and must stay false. A retained job would be
	// redelivered every time the ESP32 reconnects, so one receipt would print
	// again on every power cycle for as long as it sat on the broker.
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

	d.log.Info("published", "id", id, "bytes", len(payload))

	// Deliberately not tied to ctx: the paper is moving now and will keep
	// moving whether or not anyone is still on the other end of the request.
	// Abandoning this would leave the printer claimed forever.
	go d.awaitAck(id, acks)

	return nil
}

// awaitAck records how a job ended and frees the printer for the next one.
//
// setResult does not notify on its own, so the deferred stopWaiting is what
// publishes both changes -- the result, and the printer going idle -- as one
// event rather than two, the first of which would be half true.
func (d *Device) awaitAck(id string, acks <-chan ack) {
	defer d.stopWaiting(id)

	select {
	case a := <-acks:
		d.setResult(Result{ID: id, OK: a.OK, Error: a.Error})

	case <-time.After(ackTimeout):
		// The receipt may well be in the printer right now. All that is
		// certain is that nobody said so.
		d.log.Warn("no acknowledgement", "id", id)
		d.setResult(Result{ID: id, Error: reasonNoConfirmation})
	}
}

func (d *Device) setResult(r Result) {
	d.mu.Lock()
	d.last = &r
	d.mu.Unlock()
}

// LastJob reports how the most recent job ended, and whether there has been
// one. A single slot is enough: only one job runs at a time, so the last
// result is the only one anybody can still be asking about.
func (d *Device) LastJob() (Result, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.last == nil {
		return Result{}, false
	}

	return *d.last, true
}

// wait claims the printer for one job. There is only ever one slot, because
// there is only ever one printer: a second job would interleave its bytes with
// the first, and this design has no queue to put it in.
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

	// After the unlock, not before: notify takes subMu, and keeping the two
	// locks from ever overlapping is what makes the ordering unarguable.
	d.notify()

	return ch, nil
}

func (d *Device) stopWaiting(id string) {
	d.waitMu.Lock()
	delete(d.waiting, id)
	d.waitMu.Unlock()

	d.notify()
}

// Busy reports whether a job is on the printer right now. It is what feeds the
// BUSY indicator, and it needs no extra bookkeeping: an unfinished job is
// exactly an outstanding waiter.
func (d *Device) Busy() bool {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()

	return len(d.waiting) > 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
