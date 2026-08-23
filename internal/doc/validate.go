package doc

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid document")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

type Limits struct {
	MaxRunes int
}

func (d Document) Validate(limits Limits) error {
	if utf8.RuneCountInString(d.Text) > limits.MaxRunes {
		return invalidf("text is too long, limit is %d characters", limits.MaxRunes)
	}

	// Empty document
	if strings.TrimSpace(d.Text) == "" {
		return invalidf("document is empty")
	}

	// Printable ASCII + newline
	for i, r := range d.Text {
		if r != '\n' && (r < 0x20 || r > 0x7e) {
			return invalidf("unsupported character %q at offset %d", r, i)
		}
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
