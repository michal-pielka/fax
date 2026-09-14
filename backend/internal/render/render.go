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
// text one, with the picture where the words would be. The image is
// PhotoWidth wide -- the caller validated that -- and any pixel darker than
// mid-grey prints. The browser dithered it; this only packs bits.
//
// The picture is not streamed to the head. Over a 9600 baud wire a raster
// command (GS v 0) arrives at twenty rows a second, and on any row light
// enough to print faster than that the head stops and waits; a photo came
// out in stutters from the point it got light. Instead each piece is stored
// in the printer (GS *), which buffers it as it arrives, and then printed
// from memory (GS /) at the head's own pace, in one motion. Printer memory
// holds 6144 bytes -- it says so itself when given more, in print -- which at
// full width is 128 rows, so a square photo is three pieces with a pause
// between each while the next one loads.
func RenderPhoto(img image.Image) []byte {
	var buf bytes.Buffer

	buf.Write(reset)
	writeHeader(&buf)

	b := img.Bounds()
	for y0 := 0; y0 < b.Dy(); y0 += pieceRows {
		rows := min(pieceRows, b.Dy()-y0)
		storeAndPrint(&buf, img, y0, rows)
	}

	writeFooter(&buf)
	buf.Write(feed(tailFeed))

	return buf.Bytes()
}

// pieceRows is the most rows one stored bitmap holds at full width: 6144
// bytes of bitmap over 48 bytes of width is 128 rows, y = 16 groups of eight.
// The manual claims twice that; the printer disagreed, in print.
const pieceRows = 128

// storeAndPrint emits GS * x y d... then GS / 0 for rows [y0, y0+rows) of img.
// The stored format is columns, not rows: each byte is eight dots down one
// column, top dot in the high bit, and the bytes run down each column before
// moving right. Heights are padded to a multiple of eight with white.
func storeAndPrint(buf *bytes.Buffer, img image.Image, y0, rows int) {
	b := img.Bounds()
	w := b.Dx()
	x := (w + 7) / 8    // bytes of width
	y := (rows + 7) / 8 // groups of eight rows
	data := make([]byte, x*8*y)

	for col := 0; col < w; col++ {
		for g := 0; g < y; g++ {
			var v byte
			for bit := 0; bit < 8; bit++ {
				row := g*8 + bit
				if row < rows && dark(img.At(b.Min.X+col, b.Min.Y+y0+row)) {
					v |= 0x80 >> bit
				}
			}
			data[col*y+g] = v
		}
	}

	buf.Write([]byte{0x1d, '*', byte(x), byte(y)})
	buf.Write(data)
	buf.Write([]byte{0x1d, '/', 0}) // normal size
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
