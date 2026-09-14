package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
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
		limits: doc.Paper,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func post(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	return postAs(t, path, "application/json", strings.NewReader(body))
}

func postAs(t *testing.T, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", contentType)
	newAPI().routes().ServeHTTP(rec, req)

	return rec
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestRenderPhotoEndpoint(t *testing.T) {
	rec := postAs(t, "/internal/render", "image/png", bytes.NewReader(pngOf(t, doc.PhotoWidth, 30)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var out renderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Thirty rows fit one stored piece: defined once, printed once.
	if n := bytes.Count(out.Payload, []byte{0x1d, '*', 48}); n != 1 {
		t.Errorf("store command appears %d times, want 1", n)
	}
	if n := bytes.Count(out.Payload, []byte{0x1d, '/', 0}); n != 1 {
		t.Errorf("print command appears %d times, want 1", n)
	}
}

func TestRenderPhotoRejects(t *testing.T) {
	tests := map[string][]byte{
		"wrong width": pngOf(t, doc.PhotoWidth-1, 10),
		"too tall":    pngOf(t, doc.PhotoWidth, doc.PhotoMaxRows+1),
		"not a png":   []byte("hello"),
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if rec := postAs(t, "/internal/render", "image/png", bytes.NewReader(body)); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRenderRefusesOtherContentTypes(t *testing.T) {
	if rec := postAs(t, "/internal/render", "text/plain", strings.NewReader("hi")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
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

	// What the bytes mean is internal/render's business. This checks only that
	// the document arrived and that non-UTF-8 bytes survived JSON intact.
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
		"underline int":  `{"text":"hi","spans":[{"start":0,"end":1,"style":{"underline":2}}]}`,
		"too long":       `{"text":"` + strings.Repeat("a", doc.Cols*doc.Rows+1) + `"}`,
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
