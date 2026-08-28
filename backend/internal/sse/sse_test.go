package sse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadSkipsEverythingButData(t *testing.T) {
	stream := strings.Join([]string{
		": ping",          // a comment
		"",                //
		"data: {\"a\":1}", // one space after the colon, which is not content
		"",
		"event: named",   // a field we do not use
		"data:{\"b\":2}", // no space at all, also legal
		"",
	}, "\n")

	var got []string
	if err := Read(strings.NewReader(stream), func(b []byte) error {
		got = append(got, string(b))

		return nil
	}); err != nil {
		t.Fatalf("Read: %v", err)
	}

	want := []string{`{"a":1}`, `{"b":2}`}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A handler that gives up should stop the stream rather than keep reading a
// connection nobody is listening to.
func TestReadStopsWhenTheHandlerFails(t *testing.T) {
	boom := errors.New("boom")
	calls := 0

	err := Read(strings.NewReader("data: 1\n\ndata: 2\n\n"), func([]byte) error {
		calls++

		return boom
	})

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	if calls != 1 {
		t.Errorf("handler called %d times, want 1", calls)
	}
}

func TestWriterFraming(t *testing.T) {
	rec := httptest.NewRecorder()

	s, err := Start(rec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := s.Send(map[string]bool{"ok": true}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if err := s.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	// The blank line is the message terminator. Without it a client buffers
	// the event forever and the page simply never updates.
	want := "data: {\"ok\":true}\n\n: ping\n\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// httptest.ResponseRecorder does not implement the deadline setter, so Start
// has to tolerate that rather than refusing to stream.
func TestStartToleratesNoDeadlineSupport(t *testing.T) {
	var w http.ResponseWriter = httptest.NewRecorder()

	if _, err := Start(w); err != nil {
		t.Fatalf("Start on a recorder: %v", err)
	}
}
