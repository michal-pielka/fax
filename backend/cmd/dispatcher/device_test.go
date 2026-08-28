package main

import (
	"errors"
	"testing"
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

// Until the device says otherwise, nothing can be printed. Defaulting to
// "online" would have the gateway cheerfully accepting jobs into the void
// every time the dispatcher restarted.
func TestZeroStateIsUnprintable(t *testing.T) {
	var s State

	if s.Online || s.Paper {
		t.Fatalf("zero State = %+v, want offline with no paper", s)
	}
}

func TestStateRoundTrip(t *testing.T) {
	d := &Device{}

	if got := d.State(); got.Online {
		t.Errorf("fresh Device reports online")
	}

	d.setState(State{Online: true, Paper: true})

	if got := d.State(); !got.Online || !got.Paper {
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

	if !d.Busy() {
		t.Error("Busy() is false with a job outstanding")
	}

	if _, err := d.wait("second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second wait err = %v, want ErrBusy", err)
	}

	d.stopWaiting("first")

	if d.Busy() {
		t.Error("Busy() is true after the job finished")
	}

	if _, err := d.wait("third"); err != nil {
		t.Fatalf("wait after release: %v", err)
	}
}

// A reason code the dispatcher does not recognise must still be an error.
// Reporting success because the firmware said something unexpected is the one
// outcome the whole acknowledgement exists to prevent.
func TestAckError(t *testing.T) {
	tests := []struct {
		name string
		a    ack
		want error
	}{
		{"printed", ack{OK: true}, nil},
		{"out of paper", ack{Error: "no_paper"}, ErrNoPaper},
		{"never confirmed", ack{Error: "no_confirmation"}, ErrNoAck},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ackError(tt.a); !errors.Is(got, tt.want) {
				t.Fatalf("ackError(%+v) = %v, want %v", tt.a, got, tt.want)
			}
		})
	}

	if ackError(ack{Error: "something new"}) == nil {
		t.Error("an unrecognised reason reported success")
	}
}
