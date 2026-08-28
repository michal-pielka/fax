// Package sse writes and reads Server-Sent Events.
//
// Both services need it and for opposite reasons: the dispatcher streams state
// changes to the gateway, and the gateway streams them on to browsers. The
// gateway is therefore a reader of one stream and the writer of many.
//
// This is the whole protocol as far as we use it: a message is one or more
// "data:" lines followed by a blank one, and a line starting with a colon is a
// comment nobody sees.
package sse

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Writer streams events down one response.
type Writer struct {
	w  io.Writer
	rc *http.ResponseController
}

// Start sends the headers and takes the response out of the server's write
// deadline. That last part is not optional: WriteTimeout covers a whole
// response, and a stream is a single response that never ends -- without this
// every stream dies exactly WriteTimeout after it opens, which looks like
// everything working perfectly for forty seconds.
func Start(w http.ResponseWriter) (*Writer, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Caddy does not buffer streaming responses, so this changes nothing
	// today. It is here for the day something else is in front.
	h.Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)

	// ErrNotSupported is tolerated rather than fatal: a writer without a
	// deadline to clear has no deadline to die of either, which is what a test
	// recorder and some middleware look like. Any other failure is real.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, fmt.Errorf("clear write deadline: %w", err)
	}

	w.WriteHeader(http.StatusOK)

	s := &Writer{w: w, rc: rc}

	return s, s.flush()
}

// Send writes one event as JSON.
func (s *Writer) Send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	// json.Marshal never emits a raw newline, so the payload cannot break the
	// framing by accident.
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}

	return s.flush()
}

// Ping writes a comment. Idle connections get reaped by phone radios and NAT
// tables; this keeps the pipe warm without the client seeing an event.
func (s *Writer) Ping() error {
	if _, err := io.WriteString(s.w, ": ping\n\n"); err != nil {
		return err
	}

	return s.flush()
}

// flush pushes bytes past Go's buffering. Without it events arrive in clumps,
// or not at all.
func (s *Writer) flush() error {
	return s.rc.Flush()
}

// Read calls onData with the payload of every event on the stream, and returns
// when the stream ends or onData fails.
//
// Comments and field names other than "data" are skipped: we produce both ends
// of this, so the full grammar would be code that never runs.
func Read(r io.Reader, onData func([]byte) error) error {
	sc := bufio.NewScanner(r)
	// A state event is well under a hundred bytes. Anything approaching this
	// is a stream that is not ours.
	sc.Buffer(make([]byte, 0, 4<<10), 64<<10)

	for sc.Scan() {
		data, ok := payload(sc.Bytes())
		if !ok {
			continue
		}

		if err := onData(data); err != nil {
			return err
		}
	}

	return sc.Err()
}

func payload(line []byte) ([]byte, bool) {
	const field = "data:"

	if len(line) < len(field) || string(line[:len(field)]) != field {
		return nil, false
	}

	// One optional space after the colon is part of the format, not content.
	data := line[len(field):]
	if len(data) > 0 && data[0] == ' ' {
		data = data[1:]
	}

	return data, true
}
