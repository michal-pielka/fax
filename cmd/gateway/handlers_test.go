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

	"github.com/michal-pielka/fax/server/internal/doc"
)

type fakeRenderer struct {
	payload []byte
	err     error
	calls   int
	got     doc.Document
}

func (f *fakeRenderer) Render(_ context.Context, d doc.Document) ([]byte, error) {
	f.calls++
	f.got = d

	return f.payload, f.err
}

type fakeDispatcher struct {
	err     error
	state   State
	calls   int
	gotID   string
	gotPayl []byte
}

func (f *fakeDispatcher) Print(_ context.Context, id string, payload []byte) error {
	f.calls++
	f.gotID = id
	f.gotPayl = payload

	return f.err
}

func (f *fakeDispatcher) State(context.Context) (State, error) {
	return f.state, f.err
}

func newAPI(r Renderer, d Dispatcher) *api {
	return &api{
		renderer:   r,
		dispatcher: d,
		limits:     doc.Limits{MaxRunes: 255},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func do(t *testing.T, a *api, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))

	return rec
}

const validBody = `{"text":"hello"}`

func TestPrintHappyPath(t *testing.T) {
	r := &fakeRenderer{payload: []byte("\x1b@hello\x1bd\x03")}
	d := &fakeDispatcher{}

	rec := do(t, newAPI(r, d), http.MethodPost, "/api/print", validBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out["id"] == "" {
		t.Error("want a job id in the response")
	}

	// The id the caller is told must be the one the firmware will dedupe on,
	// or a lost job cannot be traced to the receipt it belongs to.
	if d.gotID != out["id"] {
		t.Errorf("dispatcher got id %q, response said %q", d.gotID, out["id"])
	}

	// The gateway must forward exactly what the renderer produced -- it has no
	// business understanding or altering ESC/POS.
	if string(d.gotPayl) != string(r.payload) {
		t.Errorf("dispatcher got %q, renderer produced %q", d.gotPayl, r.payload)
	}

	if r.got.Text != "hello" {
		t.Errorf("renderer got %q, want the submitted document", r.got.Text)
	}
}

func TestPrintRejectsBadDocument(t *testing.T) {
	tests := map[string]string{
		"malformed json": `{"text":`,
		"unknown field":  `{"text":"hi","colour":"red"}`,
		"empty":          `{"text":"   "}`,
		"non ascii":      `{"text":"Kraków"}`,
		"span past end":  `{"text":"hi","spans":[{"start":0,"end":99,"style":{"bold":true}}]}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			r := &fakeRenderer{}
			d := &fakeDispatcher{}

			rec := do(t, newAPI(r, d), http.MethodPost, "/api/print", body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}

			// Bad input must cost nothing downstream.
			if r.calls != 0 || d.calls != 0 {
				t.Errorf("renderer called %d, dispatcher %d; want 0 and 0", r.calls, d.calls)
			}
		})
	}
}

// A document that will not render must never reach the dispatcher: there is no
// point occupying a printer to discover the input was bad.
func TestPrintDoesNotDispatchWhenRenderFails(t *testing.T) {
	r := &fakeRenderer{err: &upstreamError{Service: "renderer", Status: 500}}
	d := &fakeDispatcher{}

	rec := do(t, newAPI(r, d), http.MethodPost, "/api/print", validBody)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}

	if d.calls != 0 {
		t.Errorf("dispatcher called %d times, want 0", d.calls)
	}
}

func TestUpstreamStatusMapping(t *testing.T) {
	tests := []struct {
		name string
		from int
		want int
	}{
		{"out of paper passes through", http.StatusConflict, http.StatusConflict},
		{"offline passes through", http.StatusServiceUnavailable, http.StatusServiceUnavailable},
		{"no ack passes through", http.StatusGatewayTimeout, http.StatusGatewayTimeout},
		{"rejected document passes through", http.StatusBadRequest, http.StatusBadRequest},
		// A broken dispatcher is not the caller's fault, and says nothing
		// about the printer.
		{"internal error becomes 502", http.StatusInternalServerError, http.StatusBadGateway},
		{"teapot becomes 502", http.StatusTeapot, http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDispatcher{err: &upstreamError{
				Service: "dispatcher", Status: tt.from, Msg: "upstream said so",
			}}

			rec := do(t, newAPI(&fakeRenderer{}, d), http.MethodPost, "/api/print", validBody)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestState(t *testing.T) {
	d := &fakeDispatcher{state: State{Online: true, Paper: false}}

	rec := do(t, newAPI(&fakeRenderer{}, d), http.MethodGet, "/api/state", "")

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

func TestHealthNeedsNoUpstream(t *testing.T) {
	if rec := do(t, newAPI(&fakeRenderer{}, &fakeDispatcher{}), http.MethodGet, "/api/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRemovedAndMismatchedRoutes(t *testing.T) {
	tests := []struct {
		method, path string
		want         int
	}{
		// Printing is synchronous now, so there is no job resource to fetch.
		{http.MethodGet, "/api/jobs/abc", http.StatusNotFound},
		{http.MethodPost, "/api/jobs", http.StatusNotFound},
		{http.MethodGet, "/api/print", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/state", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := do(t, newAPI(&fakeRenderer{}, &fakeDispatcher{}), tt.method, tt.path, "")
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestBodyTooLargeIsRejected(t *testing.T) {
	huge := `{"text":"` + strings.Repeat("a", maxBody) + `"}`

	if rec := do(t, newAPI(&fakeRenderer{}, &fakeDispatcher{}), http.MethodPost, "/api/print", huge); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The handler tests use fakes, so this covers the real client: an upstream's
// status has to survive the trip, or the mapping above is meaningless.
func TestClientPreservesUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusConflict, "printer is out of paper")
	}))
	defer srv.Close()

	err := NewDispatcherClient(srv.URL, srv.Client()).Print(context.Background(), "abc", []byte("x"))

	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want an *upstreamError", err)
	}

	if ue.Status != http.StatusConflict || ue.Msg != "printer is out of paper" {
		t.Errorf("got %d %q", ue.Status, ue.Msg)
	}
}

// ESC/POS is not valid UTF-8, so the payload has to survive JSON as bytes.
func TestRendererClientPreservesPayloadBytes(t *testing.T) {
	want := []byte{0x1b, '@', 0x00, 0xff, 'h', 'i'}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, renderResponse{Payload: want})
	}))
	defer srv.Close()

	got, err := NewRendererClient(srv.URL, srv.Client()).
		Render(context.Background(), doc.Document{Text: "hi"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("payload = %v, want %v", got, want)
	}
}

// A service that is down is not the same as one reporting on the printer, and
// must not be mistaken for a deliberate status.
func TestDeadUpstreamIsNotAnUpstreamStatus(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close()

	_, err := NewDispatcherClient(url, http.DefaultClient).State(context.Background())
	if err == nil {
		t.Fatal("want an error from a closed upstream")
	}

	var ue *upstreamError
	if errors.As(err, &ue) {
		t.Errorf("a transport failure became %v; it has no upstream status", ue)
	}
}
