package doc

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
)

var ErrInvalid = errors.New("invalid document")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Validate is the server's own word on whether a text receipt fits the paper.
// The frontend enforces the same rules as a courtesy; a caller with curl gets
// exactly the same answer from here.
func (d Document) Validate(l Limits) error {
	if strings.TrimSpace(d.Text) == "" {
		return invalidf("document is empty")
	}

	// Printable ASCII plus newline: all the printer's font has, and what
	// makes byte offsets below equal character positions.
	for i, r := range d.Text {
		if r != '\n' && (r < 0x20 || r > 0x7e) {
			return invalidf("unsupported character %q at offset %d", r, i)
		}
	}

	if rows := RowsOf(d.Text, l.Cols); rows > l.Rows {
		return invalidf("text is %d rows long, the paper has %d", rows, l.Rows)
	}

	n := len(d.Text)
	for i, s := range d.Spans {
		if s.Start < 0 || s.Start >= s.End || s.End > n {
			return invalidf("span %d: range [%d,%d) outside text of %d characters",
				i, s.Start, s.End, n)
		}
	}

	return nil
}

// RowsOf is the rows a text occupies once the printer hard-wraps it at cols.
// An empty line is still a row. The frontend does the same sum.
func RowsOf(text string, cols int) int {
	rows := 0

	for _, line := range strings.Split(text, "\n") {
		rows += max(1, (len(line)+cols-1)/cols)
	}

	return rows
}

// ValidatePhoto reads a PNG's header -- and only its header -- and says
// whether it is a picture this printer can take: exactly PhotoWidth wide and
// no taller than PhotoMaxRows. Nothing past the header is decoded until the
// size is known good, so a hostile file costs the caller a 400 and us thirty
// bytes of reading.
func ValidatePhoto(r io.Reader) (image.Config, error) {
	cfg, err := png.DecodeConfig(r)
	if err != nil {
		return cfg, invalidf("not a PNG: %v", err)
	}

	if cfg.Width != PhotoWidth {
		return cfg, invalidf("photo is %d dots wide, the paper is %d", cfg.Width, PhotoWidth)
	}

	if cfg.Height < 1 || cfg.Height > PhotoMaxRows {
		return cfg, invalidf("photo is %d rows tall, the limit is %d", cfg.Height, PhotoMaxRows)
	}

	return cfg, nil
}
