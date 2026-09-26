package doc

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestRowsOf(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"", 1},
		{"hello", 1},
		{strings.Repeat("a", 32), 1},
		{strings.Repeat("a", 33), 2},
		{"a\nb\nc", 3},
		{"a\n\nb", 3},
		{strings.Repeat("a", 65) + "\nb", 4},
	}

	for _, tt := range tests {
		if got := RowsOf(tt.text, 32); got != tt.want {
			t.Errorf("RowsOf(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

// The frontend stops at nine rows; the server must stop a caller who skipped
// the frontend at the same place.
func TestValidateRows(t *testing.T) {
	full := strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 32)+"\n", 9), "\n")
	if err := (Document{Text: full}).Validate(Paper); err != nil {
		t.Errorf("a full page is invalid: %v", err)
	}

	// The same characters with no newlines wrap into ten rows.
	over := strings.Repeat("a", 32*9+1)
	if err := (Document{Text: over}).Validate(Paper); !errors.Is(err, ErrInvalid) {
		t.Errorf("ten rows accepted: %v", err)
	}

	tall := strings.Repeat("a\n", 9) + "a"
	if err := (Document{Text: tall}).Validate(Paper); !errors.Is(err, ErrInvalid) {
		t.Errorf("ten lines accepted: %v", err)
	}
}

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestValidatePhoto(t *testing.T) {
	tests := []struct {
		name string
		png  []byte
		ok   bool
	}{
		{"exact width, short", encodePNG(t, PhotoWidth, 10), true},
		{"exact width, tallest", encodePNG(t, PhotoWidth, PhotoMaxRows), true},
		{"too tall", encodePNG(t, PhotoWidth, PhotoMaxRows+1), false},
		{"too narrow", encodePNG(t, PhotoWidth-1, 10), false},
		{"too wide", encodePNG(t, PhotoWidth+1, 10), false},
		{"not a PNG", []byte("GIF89a not really"), false},
		{"empty", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidatePhoto(bytes.NewReader(tt.png))
			if tt.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}

			if !tt.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

// A header that lies about its size is a 400, not a decode: the check must
// not need the pixel data at all.
func TestValidatePhotoReadsOnlyTheHeader(t *testing.T) {
	whole := encodePNG(t, PhotoWidth, 10)
	// The IHDR chunk ends 33 bytes in; everything after is pixel data.
	if _, err := ValidatePhoto(bytes.NewReader(whole[:33])); err != nil {
		t.Fatalf("header alone was not enough: %v", err)
	}
}

// Decode is the one entry point for a text receipt, so a caller can treat any
// error from it as the sender's fault.
func TestDecodeErrorsAreInvalid(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":     `{"text":`,
		"unknown field": `{"text":"hi","colour":"red"}`,
		"empty text":    `{"text":"  "}`,
		"bad span":      `{"text":"hi","spans":[{"start":0,"end":9}]}`,
	} {
		if _, err := Decode(strings.NewReader(body), Paper); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	d, err := Decode(strings.NewReader(`{"text":"hi"}`), Paper)
	if err != nil || d.Text != "hi" {
		t.Errorf("valid document: %+v, %v", d, err)
	}
}
