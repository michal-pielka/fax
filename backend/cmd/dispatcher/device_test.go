package main

import (
	"errors"
	"testing"
	"time"
)

func TestNewTopics(t *testing.T) {
	got := newTopics("printer-1")

	if got.job != "fax/printer-1/job" {
		t.Errorf("job topic = %q", got.job)
	}

	if got.state != "fax/printer-1/state" {
		t.Errorf("state topic = %q", got.state)
	}

	if got.ack != "fax/printer-1/ack" {
		t.Errorf("ack topic = %q", got.ack)
	}
}

// Defaulting to online would have the gateway accepting jobs into the void
// every time the dispatcher restarted.
func TestZeroStateIsUnprintable(t *testing.T) {
	var s State

	if s.Online {
		t.Fatalf("zero State = %+v, want offline", s)
	}
}

func TestStateRoundTrip(t *testing.T) {
	d := &Device{}

	if got := d.State(); got.Online {
		t.Errorf("fresh Device reports online")
	}

	d.setState(State{Online: true})

	if got := d.State(); !got.Online {
		t.Errorf("State() = %+v after setState", got)
	}
}

func newWaiting() *Device {
	return &Device{waiting: make(map[string]chan ack)}
}

// The printer is one physical thing. A second job would interleave its bytes
// with the first, and there is no queue in this design to put it in.
func TestOnlyOneJobAtATime(t *testing.T) {
	d := newWaiting()

	if _, err := d.wait("first"); err != nil {
		t.Fatalf("first wait: %v", err)
	}

	if !d.busy() {
		t.Error("Busy() is false with a job outstanding")
	}

	if _, err := d.wait("second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second wait err = %v, want ErrBusy", err)
	}

	d.stopWaiting("first")

	if d.busy() {
		t.Error("Busy() is true after the job finished")
	}

	if _, err := d.wait("third"); err != nil {
		t.Fatalf("wait after release: %v", err)
	}
}

// The firmware's answer becomes the request's error, so the caller learns how
// the job ended without a second round trip.
func TestAwaitAckMapsTheAnswer(t *testing.T) {
	tests := []struct {
		name string
		ack  ack
		want error
	}{
		{"ok", ack{OK: true}, nil},
		{"no paper measured by the firmware", ack{Error: reasonNoPaper}, ErrNoPaper},
		{"printer went silent", ack{Error: reasonNoConfirmation}, ErrNoConfirmation},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newWaiting()

			acks, err := d.wait("job")
			if err != nil {
				t.Fatalf("wait: %v", err)
			}

			ch := d.waiting["job"]
			ch <- tt.ack

			if got := d.awaitAck("job", acks, time.Second); !errors.Is(got, tt.want) {
				t.Errorf("awaitAck = %v, want %v", got, tt.want)
			}

			// Whatever the answer, the printer is free for the next job.
			if d.busy() {
				t.Error("printer still claimed after the ack")
			}
		})
	}
}

// busy needs no bookkeeping: an unfinished job is exactly an outstanding
// waiter.
func (d *Device) busy() bool {
	d.waitMu.Lock()
	defer d.waitMu.Unlock()

	return len(d.waiting) > 0
}
