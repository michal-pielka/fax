package render

import (
	"bytes"
	"image"
	"image/color"
	"strings"

	"github.com/michal-pielka/fax/server/internal/doc"
)

var (
	reset   = []byte{0x1b, '@'}
	boldOn  = []byte{0x1b, 'E', 1}
	boldOff = []byte{0x1b, 'E', 0}
	// ESC - n: n is the underline's thickness in dots. Two, always: one dot
	// barely registers on thermal paper.
	underlineOn  = []byte{0x1b, '-', 2}
	underlineOff = []byte{0x1b, '-', 0}
	invertOn     = []byte{0x1d, 'B', 1}
	invertOff    = []byte{0x1d, 'B', 0}

	// Frame only. Documents carry bold and underline and nothing else: the
	// frame is ours, the contents are theirs.
	alignLeft   = []byte{0x1b, 'a', 0}
	alignCentre = []byte{0x1b, 'a', 1}

	// GS ! n, where the high nibble is width-1 and the low nibble height-1.
	// 0x11 is double both ways.
	sizeNormal = []byte{0x1d, '!', 0x00}
	sizeDouble = []byte{0x1d, '!', 0x11}
)

// cols is the printer's character width at the default font. The title prints
// at double width, so it has half as many.
const cols = doc.Cols

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
			buf.Write(pick(want.Underline, underlineOn, underlineOff))
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

	if cur.Underline {
		buf.Write(underlineOff)
	}

	if cur.Invert {
		buf.Write(invertOff)
	}

	writeFooter(&buf)
	buf.Write(feed(tailFeed))

	return buf.Bytes()
}

// RenderPhoto returns the bytes for a picture receipt: the same frame as a
// text one, with rows of dots where the words would be. The image is
// PhotoWidth wide -- the caller validated that -- and any pixel darker than
// mid-grey prints. The browser dithered it; this only packs bits.
func RenderPhoto(img image.Image) []byte {
	var buf bytes.Buffer

	buf.Write(reset)
	writeHeader(&buf)
	raster(&buf, img)
	writeFooter(&buf)
	buf.Write(feed(tailFeed))

	return buf.Bytes()
}

// rasterBand is rows per GS v 0 command. Small bands keep the printer's
// buffer shallow and let it start moving paper before the whole picture has
// arrived over a 9600 baud wire.
const rasterBand = 24

// raster emits GS v 0 m xL xH yL yH d1..dk: x is bytes per row, y rows, then
// the rows themselves, eight pixels a byte, leftmost pixel in the high bit,
// one for black.
func raster(buf *bytes.Buffer, img image.Image) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	perRow := (w + 7) / 8

	for y0 := 0; y0 < h; y0 += rasterBand {
		n := min(rasterBand, h-y0)
		buf.Write([]byte{0x1d, 'v', '0', 0, byte(perRow), byte(perRow >> 8), byte(n), byte(n >> 8)})

		row := make([]byte, perRow)
		for y := y0; y < y0+n; y++ {
			clear(row)

			for x := range w {
				if dark(img.At(b.Min.X+x, b.Min.Y+y)) {
					row[x/8] |= 0x80 >> (x % 8)
				}
			}

			buf.Write(row)
		}
	}
}

// dark is the one decision made about a pixel. A dithered picture is already
// pure black and white, so this only matters for a caller who sent grey.
func dark(c color.Color) bool {
	return color.GrayModel.Convert(c).(color.Gray).Y < 128
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
			s.Underline = s.Underline || sp.Style.Underline
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
