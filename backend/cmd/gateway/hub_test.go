package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func testHub(t *testing.T, d Dispatcher) *hub {
	t.Helper()

	return newHub(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSubscribersGetTheStateOnArrival(t *testing.T) {
	h := testHub(t, &fakeDispatcher{})
	h.broadcast(State{Online: true, Paper: true})

	_, current, release, ok := h.subscribe()
	if !ok {
		t.Fatal("subscribe refused")
	}
	defer release()

	// A page that opens between two changes must not sit with dark lamps
	// until the next one happens.
	if !current.Online || !current.Paper {
		t.Fatalf("opening state = %+v, want the last broadcast", current)
	}
}

func TestBroadcastReachesEveryClient(t *testing.T) {
	h := testHub(t, &fakeDispatcher{})

	a, _, releaseA, _ := h.subscribe()
	defer releaseA()

	b, _, releaseB, _ := h.subscribe()
	defer releaseB()

	h.broadcast(State{Online: true, Busy: true})

	for i, ch := range []<-chan State{a, b} {
		select {
		case got := <-ch:
			if !got.Busy {
				t.Errorf("client %d got %+v", i, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("client %d received nothing", i)
		}
	}
}

// One stalled phone must not hold up everyone else, so a client with an unread
// state is skipped rather than waited for.
func TestBroadcastDoesNotBlockOnASlowClient(t *testing.T) {
	h := testHub(t, &fakeDispatcher{})

	ch, _, release, _ := h.subscribe()
	defer release()

	done := make(chan struct{})

	go func() {
		defer close(done)

		for range 5 {
			h.broadcast(State{Online: true})
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on a client that never read")
	}

	// It kept the newest rather than a backlog of stale ones.
	if got := <-ch; !got.Online {
		t.Errorf("buffered state = %+v", got)
	}
}

func TestReleasedClientsStopReceiving(t *testing.T) {
	h := testHub(t, &fakeDispatcher{})

	_, _, release, _ := h.subscribe()
	release()

	h.mu.Lock()
	n := len(h.clients)
	h.mu.Unlock()

	if n != 0 {
		t.Errorf("%d clients left after release", n)
	}
}

func TestStreamsAreCapped(t *testing.T) {
	h := testHub(t, &fakeDispatcher{})

	for i := range maxStreams {
		if _, _, _, ok := h.subscribe(); !ok {
			t.Fatalf("refused at %d, below the cap", i)
		}
	}

	// A public endpoint holding a connection and a goroutine per caller needs
	// a ceiling, or it is the cheapest way to exhaust the box.
	if _, _, _, ok := h.subscribe(); ok {
		t.Error("accepted a stream past the cap")
	}
}

func TestFollowBroadcastsWhatItReads(t *testing.T) {
	d := &fakeDispatcher{events: "data: {\"online\":true,\"paper\":true,\"busy\":false}\n\n"}
	h := testHub(t, d)

	delivered, err := h.follow(context.Background())
	if err != nil {
		t.Fatalf("follow: %v", err)
	}

	if !delivered {
		t.Error("follow reported nothing delivered")
	}

	h.mu.Lock()
	got := h.last
	h.mu.Unlock()

	if !got.Online || !got.Paper {
		t.Errorf("last = %+v", got)
	}
}

// A single unparseable event should not tear down a working stream.
func TestFollowSurvivesAMalformedEvent(t *testing.T) {
	d := &fakeDispatcher{events: "data: not json\n\ndata: {\"online\":true}\n\n"}
	h := testHub(t, d)

	if _, err := h.follow(context.Background()); err != nil {
		t.Fatalf("follow: %v", err)
	}

	h.mu.Lock()
	got := h.last
	h.mu.Unlock()

	if !got.Online {
		t.Errorf("last = %+v, want the event after the bad one", got)
	}
}
