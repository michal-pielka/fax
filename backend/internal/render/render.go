package render

import (
	"bytes"
	"strings"

	"github.com/michal-pielka/fax/server/internal/doc"
)

var (
	reset     = []byte{0x1b, '@'}
	boldOn    = []byte{0x1b, 'E', 1}
	boldOff   = []byte{0x1b, 'E', 0}
	invertOn  = []byte{0x1d, 'B', 1}
	invertOff = []byte{0x1d, 'B', 0}

	// Frame only. Documents carry bold and underline and nothing else: the
	// frame is ours, the contents are theirs.
	alignLeft   = []byte{0x1b, 'a', 0}
	alignCentre = []byte{0x1b, 'a', 1}

	// GS ! n, where the high nibble is width-1 and the low nibble height-1.
	// 0x11 is double both ways.
	sizeNormal = []byte{0x1d, '!', 0x00}
	sizeDouble = []byte{0x1d, '!', 0x11}
)

// underline emits ESC - n: 0 off, 1 one dot thick, 2 two dots. The document
// carries the same numbers, so there is nothing to translate.
func underline(dots int) []byte {
	return []byte{0x1b, '-', byte(dots)}
}

// cols is the printer's character width at the default font. The title prints
// at double width, so it has half as many.
const cols = 32

// The frame around every receipt. Lines must fit cols, the title cols/2;
// TestFrameFitsThePaper fails the build otherwise.
var (
	title  = "FAX"
	header = []string{
		"THE SLOWEST SOCIAL NETWORK",
	}
	footer = []string{
		"COMMITTED TO PHYSICAL MEDIA",
		"*** fax.pielka.sh ***",
	}
)

// tailFeed clears the tear bar. The head sits centimetres above it, so without
// this the last lines stay inside the printer and look dropped.
const tailFeed = 3

// feed emits ESC d n: print the buffer and advance n lines.
func feed(lines byte) []byte {
	return []byte{0x1b, 'd', lines}
}

// Render returns the bytes for d. No error: a validated document has nothing
// left to reject. Text goes out as-is, and the printer does its own wrapping.
func Render(d doc.Document) []byte {
	var buf bytes.Buffer

	// The printer keeps the last job's state; without a reset a receipt comes
	// out wearing someone else's bold.
	buf.Write(reset)

	writeHeader(&buf)

	var cur doc.Style

	// Validate restricts text to ASCII, so byte offsets are character
	// positions. Untrue the moment codepages happen.
	for i := range len(d.Text) {
		want := styleAt(d.Spans, i)

		if want.Bold != cur.Bold {
			buf.Write(pick(want.Bold, boldOn, boldOff))
		}

		if want.Underline != cur.Underline {
			buf.Write(underline(want.Underline))
		}

		if want.Invert != cur.Invert {
			buf.Write(pick(want.Invert, invertOn, invertOff))
		}

		cur = want

		buf.WriteByte(d.Text[i])
	}

	// Leave the printer as it was found. Styles persist across jobs, so a
	// receipt ending mid-bold would tint the next one.
	if cur.Bold {
		buf.Write(boldOff)
	}

	if cur.Underline != 0 {
		buf.Write(underline(0))
	}

	if cur.Invert {
		buf.Write(invertOff)
	}

	writeFooter(&buf)
	buf.Write(feed(tailFeed))

	return buf.Bytes()
}

func writeHeader(buf *bytes.Buffer) {
	buf.Write(alignCentre)

	buf.Write(sizeDouble)
	buf.WriteString(title)
	buf.WriteByte('\n')
	buf.Write(sizeNormal)

	for _, line := range header {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	// Back to the left before the body: the document is the sender's, and
	// centring their text would be us editing it.
	buf.Write(alignLeft)
	buf.WriteString(divider())
	buf.WriteByte('\n')
}

func writeFooter(buf *bytes.Buffer) {
	// The body may or may not end in a newline, and a divider sharing a line
	// with the last words of a message looks like a mistake.
	buf.WriteByte('\n')
	buf.WriteString(divider())
	buf.WriteByte('\n')

	buf.Write(alignCentre)

	for _, line := range footer {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	// Leave the printer as it was found, so the next job starts from a known
	// state even if it somehow skips the reset.
	buf.Write(alignLeft)
}

func divider() string {
	return strings.Repeat("-", cols)
}

// styleAt unions every span covering offset i. Per character rather than per
// boundary, so overlapping and unsorted spans need no special handling.
func styleAt(spans []doc.Span, i int) doc.Style {
	var s doc.Style

	for _, sp := range spans {
		if i >= sp.Start && i < sp.End {
			s.Bold = s.Bold || sp.Style.Bold
			s.Underline = max(s.Underline, sp.Style.Underline)
			s.Invert = s.Invert || sp.Style.Invert
		}
	}

	return s
}

func pick(on bool, yes, no []byte) []byte {
	if on {
		return yes
	}

	return no
}
