package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/michal-pielka/fax/server/internal/sse"
)

const (
	// A held connection and a goroutine each, on an endpoint with no rate
	// limiting: the cheapest way to exhaust this box.
	maxStreams = 64

	// Doubles up to the maximum, so an hour of downtime is not hammered.
	minBackoff = 500 * time.Millisecond
	maxBackoff = 15 * time.Second
)

// hub holds one stream open to the dispatcher and fans it out to every
// browser. Nothing polls: state is copied once per change, not per client.
type hub struct {
	dispatcher Dispatcher
	log        *slog.Logger

	mu      sync.Mutex
	clients map[chan State]struct{}
	last    State
}

func newHub(d Dispatcher, log *slog.Logger) *hub {
	return &hub{
		dispatcher: d,
		log:        log,
		clients:    make(map[chan State]struct{}),
	}
}

// subscribe registers a browser and hands back the state to open with, so a
// page never renders with nothing in it while waiting for the first change.
func (h *hub) subscribe() (<-chan State, State, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.clients) >= maxStreams {
		return nil, State{}, nil, false
	}

	// Buffered by one: broadcast must never block on a slow client, and a
	// client that is behind wants the newest state anyway, not a backlog.
	ch := make(chan State, 1)
	h.clients[ch] = struct{}{}

	return ch, h.last, func() { h.unsubscribe(ch) }, true
}

func (h *hub) unsubscribe(ch chan State) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, ch)
}

// broadcast pushes to everyone, skipping a client whose buffer is full: what
// it is about to read is newer than what it missed.
func (h *hub) broadcast(s State) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.last = s

	for ch := range h.clients {
		select {
		case ch <- s:
		default:
		}
	}
}

// run keeps the dispatcher's stream open for the life of the process,
// reconnecting when it drops. It is started once, at boot.
func (h *hub) run(ctx context.Context) {
	backoff := minBackoff

	for ctx.Err() == nil {
		delivered, err := h.follow(ctx)
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			h.log.Warn("dispatcher event stream ended", "err", err)
		}

		// Blind now, so say so. The zero State is offline with no paper;
		// leaving the last one on screen would be a guess.
		h.broadcast(State{})

		// A stream that carried something was real, so reset the backoff.
		if delivered {
			backoff = minBackoff
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// follow reads one connection until it ends, reporting whether it carried
// anything at all.
func (h *hub) follow(ctx context.Context) (bool, error) {
	body, err := h.dispatcher.Events(ctx)
	if err != nil {
		return false, err
	}
	defer body.Close()

	h.log.Info("following dispatcher state")

	var delivered bool

	err = sse.Read(body, func(data []byte) error {
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			// One malformed event is not a reason to tear down a working
			// stream; the next one is a moment away.
			h.log.Error("bad state event", "err", err)

			return nil
		}

		delivered = true
		h.broadcast(s)

		return nil
	})

	return delivered, err
}
