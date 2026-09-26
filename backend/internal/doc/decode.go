package doc

import (
	"bytes"
	"encoding/json"
	"image"
	"io"
)

// Decode reads a text receipt as JSON and validates it against l, so every
// service that accepts one applies the same rules. Unknown fields are refused:
// a typo'd field is a 400, not silent data loss. Every error wraps ErrInvalid.
func Decode(r io.Reader, l Limits) (Document, error) {
	var d Document

	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&d); err != nil {
		return d, invalidf("malformed JSON: %v", err)
	}

	return d, d.Validate(l)
}

// ReadPhoto reads a whole PNG body and checks its header with ValidatePhoto.
// The bytes come back undecoded; every error wraps ErrInvalid.
func ReadPhoto(r io.Reader) ([]byte, image.Config, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, image.Config{}, invalidf("photo too large or unreadable")
	}

	cfg, err := ValidatePhoto(bytes.NewReader(b))

	return b, cfg, err
}
