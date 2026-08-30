// Package sse writes and reads Server-Sent Events: "data:" lines ended by a
// blank one, and ":" lines as comments nobody sees.
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

// Start sends the headers and clears the write deadline. Not optional:
// WriteTimeout covers a whole response, and a stream never ends.
func Start(w http.ResponseWriter) (*Writer, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Insurance for the day something other than Caddy is in front.
	h.Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)

	// ErrNotSupported is fine: no deadline to clear means none to die of.
	// Any other failure is real.
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

// Read calls onData for every event, returning when the stream ends or onData
// fails. Only "data" is parsed: we write both ends of this.
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
