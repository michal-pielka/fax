package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePrinter stands in for the MQTT connection, so the handlers can be tested
// without a broker.
type fakePrinter struct {
	err   error
	state State
	calls int
	gotID string
	gotPL []byte
}

func (f *fakePrinter) Publish(_ context.Context, id string, payload []byte) error {
	f.calls++
	f.gotID = id
	f.gotPL = payload

	return f.err
}

func (f *fakePrinter) State() State { return f.state }

func newAPI(p Printer) *api {
	return &api{printer: p, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func do(t *testing.T, p Printer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	newAPI(p).routes().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))

	return rec
}

// The payload is base64 in JSON, so this also checks the bytes survive a round
// trip -- ESC/POS is not valid UTF-8.
const printBody = `{"id":"abc123","payload":"G0BoaRtkAw=="}`

func TestPrintPublishes(t *testing.T) {
	p := &fakePrinter{state: State{Online: true, Paper: true}}

	rec := do(t, p, http.MethodPost, "/internal/print", printBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	if p.calls != 1 {
		t.Fatalf("Publish called %d times, want 1", p.calls)
	}

	if p.gotID != "abc123" {
		t.Errorf("id = %q, want abc123", p.gotID)
	}

	if want := "\x1b@hi\x1bd\x03"; string(p.gotPL) != want {
		t.Errorf("payload = %q, want %q", p.gotPL, want)
	}

	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// "published" rather than "printed": the firmware cannot confirm paper
	// moved, and the response should not imply that it did.
	if out["status"] != "published" {
		t.Errorf("status = %q, want published", out["status"])
	}
}

func TestPrintErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"out of paper is a conflict", ErrNoPaper, http.StatusConflict},
		{"offline is unavailable", ErrOffline, http.StatusServiceUnavailable},
		// Not a statement about the printer -- the dispatcher failed at its
		// one job, so it must not be reported as a printer condition.
		{"broker failure is unavailable", errors.New("connection refused"), http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, &fakePrinter{err: tt.err}, http.MethodPost, "/internal/print", printBody)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestPrintRejectsBadRequests(t *testing.T) {
	tests := map[string]string{
		"malformed json": `{"id":`,
		"unknown field":  `{"id":"a","payload":"AA==","extra":1}`,
		"missing id":     `{"payload":"AA=="}`,
		"empty payload":  `{"id":"a","payload":""}`,
		"no payload":     `{"id":"a"}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			p := &fakePrinter{state: State{Online: true, Paper: true}}

			rec := do(t, p, http.MethodPost, "/internal/print", body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}

			// Nothing malformed should reach the broker.
			if p.calls != 0 {
				t.Errorf("Publish called %d times, want 0", p.calls)
			}
		})
	}
}

func TestState(t *testing.T) {
	rec := do(t, &fakePrinter{state: State{Online: true, Paper: false}},
		http.MethodGet, "/internal/state", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got State
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !got.Online || got.Paper {
		t.Errorf("state = %+v, want online without paper", got)
	}
}

// An offline printer is a normal condition, not an unhealthy dispatcher.
// Conflating them would have an orchestrator restarting this process every
// time the roll ran out.
func TestHealthIgnoresPrinterState(t *testing.T) {
	rec := do(t, &fakePrinter{state: State{}}, http.MethodGet, "/internal/health", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with the printer offline", rec.Code)
	}
}
