package render

import (
	"bytes"
	"strings"

	"github.com/michal-pielka/fax/server/internal/doc"
)

var (
	reset        = []byte{0x1b, '@'}
	boldOn       = []byte{0x1b, 'E', 1}
	boldOff      = []byte{0x1b, 'E', 0}
	underlineOn  = []byte{0x1b, '-', 1}
	underlineOff = []byte{0x1b, '-', 0}

	// Used only by the header and footer. Documents cannot reach these: the
	// model callers send carries bold and underline and nothing else. That
	// asymmetry is deliberate -- the frame is ours, the contents are theirs.
	alignLeft   = []byte{0x1b, 'a', 0}
	alignCentre = []byte{0x1b, 'a', 1}

	// GS ! n, where the high nibble is width-1 and the low nibble height-1.
	// 0x11 is double both ways.
	sizeNormal = []byte{0x1d, '!', 0x00}
	sizeDouble = []byte{0x1d, '!', 0x11}
)

// cols is the printer's character width at the default font. The title prints
// at double width, so it has half as many.
const cols = 32

// The frame around every receipt.
//
// Keep header and footer lines within cols characters, and the title within
// cols/2 -- TestFrameFitsThePaper fails the build otherwise.
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

// tailFeed is how many lines to advance once the text is out.
//
// The print head sits a couple of centimetres above the tear bar, so without
// this the last lines of a receipt are still inside the printer and look like
// they were dropped.
const tailFeed = 3

// feed emits ESC d n: print the buffer and advance n lines.
func feed(lines byte) []byte {
	return []byte{0x1b, 'd', lines}
}

// Render returns the bytes for d.
//
// There is no error return. A document that has passed doc.Validate has
// nothing left to reject, and writes to a bytes.Buffer cannot fail.
//
// Text is emitted as-is, including newlines: 0x0A already means "print this
// line and feed" to the printer. Nothing wraps the text either, because the
// printer wraps at its own column width. That is fine while the preview is
// CSS-based, and is the first thing to change when the preview has to match
// the paper exactly.
func Render(d doc.Document) []byte {
	var buf bytes.Buffer

	// The printer holds whatever state the last job left behind, so a receipt
	// that does not reset can come out wearing someone else's bold.
	buf.Write(reset)

	writeHeader(&buf)

	var cur doc.Style

	// Spans index bytes, and Validate restricts the text to ASCII, so byte
	// offsets and character positions are the same thing here. That stops
	// being true the moment the codepage work happens.
	for i := range len(d.Text) {
		want := styleAt(d.Spans, i)

		if want.Bold != cur.Bold {
			buf.Write(pick(want.Bold, boldOn, boldOff))
		}

		if want.Underline != cur.Underline {
			buf.Write(pick(want.Underline, underlineOn, underlineOff))
		}

		cur = want

		buf.WriteByte(d.Text[i])
	}

	// Leave the printer as it was found. Styles persist across jobs, so a
	// receipt ending mid-bold would tint the next one.
	if cur.Bold {
		buf.Write(boldOff)
	}

	if cur.Underline {
		buf.Write(underlineOff)
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

// styleAt is the union of every span covering offset i.
//
// Deciding the style per character, rather than walking span boundaries, is
// what makes overlapping and unsorted spans behave sensibly. Two overlapping
// bold spans stay bold across the join; iterating boundaries instead would
// switch bold off at the end of the first one while the second still wanted
// it. It also means Validate does not have to forbid either case.
//
// This is quadratic in (text length x span count), which is irrelevant at a
// few hundred characters and a handful of spans.
func styleAt(spans []doc.Span, i int) doc.Style {
	var s doc.Style

	for _, sp := range spans {
		if i >= sp.Start && i < sp.End {
			s.Bold = s.Bold || sp.Style.Bold
			s.Underline = s.Underline || sp.Style.Underline
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
