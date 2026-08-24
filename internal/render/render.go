package render

import (
	"bytes"

	"github.com/michal-pielka/fax/server/internal/doc"
)

var (
	reset        = []byte{0x1b, '@'}
	boldOn       = []byte{0x1b, 'E', 1}
	boldOff      = []byte{0x1b, 'E', 0}
	underlineOn  = []byte{0x1b, '-', 1}
	underlineOff = []byte{0x1b, '-', 0}
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

	buf.Write(feed(tailFeed))

	return buf.Bytes()
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
