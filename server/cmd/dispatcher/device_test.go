package main

import (
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
