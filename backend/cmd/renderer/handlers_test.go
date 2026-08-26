package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/michal-pielka/fax/server/internal/doc"
)

func newAPI() *api {
	return &api{
		limits: doc.Limits{MaxRunes: maxRunes},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	newAPI().routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))

	return rec
}

func TestRenderEndpoint(t *testing.T) {
	rec := post(t, "/internal/render", `{"text":"hi"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var out renderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// What the bytes mean is internal/render's business and is tested there.
	// This checks the two things the HTTP layer is responsible for: that the
	// document reached the renderer, and that the bytes survived JSON intact.
	// They are not valid UTF-8, so a string-typed field would have replaced
	// the high ones with U+FFFD along the way.
	if !bytes.HasPrefix(out.Payload, []byte{0x1b, '@'}) {
		t.Errorf("payload does not begin with ESC @: %q", out.Payload)
	}

	if !bytes.Contains(out.Payload, []byte("hi")) {
		t.Errorf("payload does not contain the submitted text: %q", out.Payload)
	}

	if !bytes.Contains(out.Payload, []byte{0x1d, '!', 0x11}) {
		t.Errorf("payload is missing the header's double-width title: %q", out.Payload)
	}
}

// The gateway validates too, but the renderer is a separate process and does
// not get to assume its caller behaved.
func TestRenderEndpointRejectsBadInput(t *testing.T) {
	tests := map[string]string{
		"malformed json": `{"text":`,
		"unknown field":  `{"text":"hi","colour":"red"}`,
		"empty":          `{"text":"   "}`,
		"non ascii":      `{"text":"Kraków"}`,
		"span past end":  `{"text":"hi","spans":[{"start":0,"end":99,"style":{"bold":true}}]}`,
		"too long":       `{"text":"` + strings.Repeat("a", maxRunes+1) + `"}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if rec := post(t, "/internal/render", body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newAPI().routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
