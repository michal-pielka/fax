package render

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// cat joins byte sequences, so an expected receipt reads as the sequence of
// commands it actually is rather than one opaque literal.
func cat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
}

// body is the part between the dividers, so the golden tests assert on what
// the sender wrote rather than on the frame.
func body(t *testing.T, out []byte) []byte {
	t.Helper()

	mark := []byte(divider() + "\n")

	start := bytes.Index(out, mark)
	if start < 0 {
		t.Fatalf("no opening divider in %q", out)
	}
	start += len(mark)

	end := bytes.LastIndex(out, mark)
	if end <= start {
		t.Fatalf("no closing divider in %q", out)
	}

	// writeFooter emits a newline before the closing divider.
	return bytes.TrimSuffix(out[:end], []byte("\n"))[start:]
}

func text(s string) []byte { return []byte(s) }

func bold(start, end int) doc.Span {
	return doc.Span{Start: start, End: end, Style: doc.Style{Bold: true}}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		in   doc.Document
		want []byte
	}{
		{
			name: "plain text",
			in:   doc.Document{Text: "hi"},
			want: text("hi"),
		},
		{
			name: "newlines pass straight through",
			in:   doc.Document{Text: "one\ntwo"},
			want: text("one\ntwo"),
		},
		{
			name: "bold span toggles on and off",
			in:   doc.Document{Text: "hi", Spans: []doc.Span{bold(0, 1)}},
			want: cat(boldOn, text("h"), boldOff, text("i")),
		},
		{
			name: "bold running to the end is closed before the feed",
			in:   doc.Document{Text: "hi", Spans: []doc.Span{bold(0, 2)}},
			want: cat(boldOn, text("hi"), boldOff),
		},
		{
			name: "underline is the two-dot kind",
			in: doc.Document{Text: "ab", Spans: []doc.Span{
				{Start: 1, End: 2, Style: doc.Style{Underline: true}},
			}},
			want: cat(text("a"), underlineOn, text("b"), underlineOff),
		},
		{
			name: "invert",
			in: doc.Document{Text: "ab", Spans: []doc.Span{
				{Start: 0, End: 1, Style: doc.Style{Invert: true}},
			}},
			want: cat(invertOn, text("a"), invertOff, text("b")),
		},
		{
			name: "invert running to the end is closed before the frame",
			in: doc.Document{Text: "x", Spans: []doc.Span{
				{Start: 0, End: 1, Style: doc.Style{Invert: true}},
			}},
			want: cat(invertOn, text("x"), invertOff),
		},
		{
			name: "bold and underline together emit both",
			in: doc.Document{Text: "x", Spans: []doc.Span{
				{Start: 0, End: 1, Style: doc.Style{Bold: true, Underline: true}},
			}},
			want: cat(boldOn, underlineOn, text("x"), boldOff, underlineOff),
		},
		{
			// Breaks a boundary-walking implementation: it would switch bold
			// off where the first span ends, while the second still wants it.
			name: "overlapping spans do not switch off at the join",
			in:   doc.Document{Text: "abcdef", Spans: []doc.Span{bold(0, 3), bold(2, 5)}},
			want: cat(boldOn, text("abcde"), boldOff, text("f")),
		},
		{
			name: "span order does not matter",
			in:   doc.Document{Text: "abcdef", Spans: []doc.Span{bold(4, 6), bold(0, 2)}},
			want: cat(boldOn, text("ab"), boldOff, text("cd"), boldOn, text("ef"), boldOff),
		},
		{
			name: "adjacent spans do not emit a redundant toggle",
			in:   doc.Document{Text: "abcd", Spans: []doc.Span{bold(0, 2), bold(2, 4)}},
			want: cat(boldOn, text("abcd"), boldOff),
		},
		{
			name: "a span covering nothing changes nothing",
			in:   doc.Document{Text: "ab", Spans: nil},
			want: text("ab"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := body(t, Render(tt.in))
			if !bytes.Equal(got, tt.want) {
				t.Errorf("\ngot  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// Every receipt has to start from a known state and end clear of the tear bar,
// whatever is in between.
func TestRenderAlwaysResetsAndFeeds(t *testing.T) {
	docs := []doc.Document{
		{Text: "a"},
		{Text: "a", Spans: []doc.Span{bold(0, 1)}},
		{Text: "multi\nline\ntext"},
	}

	for _, d := range docs {
		got := Render(d)

		if !bytes.HasPrefix(got, reset) {
			t.Errorf("Render(%q) does not begin with ESC @", d.Text)
		}

		if !bytes.HasSuffix(got, feed(tailFeed)) {
			t.Errorf("Render(%q) does not end with a feed", d.Text)
		}
	}
}

// The renderer is the one piece that must be reproducible: preview and paper
// come from the same call, and golden tests are worthless if it drifts.
func TestRenderIsDeterministic(t *testing.T) {
	d := doc.Document{Text: "abcdef", Spans: []doc.Span{bold(1, 3), {
		Start: 2, End: 5, Style: doc.Style{Underline: true, Invert: true},
	}}}

	first := Render(d)

	for range 100 {
		if !bytes.Equal(Render(d), first) {
			t.Fatal("Render is not deterministic")
		}
	}
}

// Whatever the document, the printed characters must survive unaltered --
// commands are added around the text, never in place of it.
func TestTextSurvivesUnaltered(t *testing.T) {
	const message = "Order #1234\nTotal: $9.99\n** thanks **"

	got := Render(doc.Document{Text: message, Spans: []doc.Span{bold(0, 5), {
		Start: 12, End: 17, Style: doc.Style{Underline: true, Invert: true},
	}}})

	if stripped := stripCommands(body(t, got)); !bytes.Equal(stripped, []byte(message)) {
		t.Errorf("text was altered:\ngot  %q\nwant %q", stripped, message)
	}
}

// stripCommands leaves only what lands on paper. ESC @ is two bytes; the rest,
// ESC and GS alike, take a parameter and are three.
func stripCommands(b []byte) []byte {
	var out []byte

	for i := 0; i < len(b); {
		if b[i] != 0x1b && b[i] != 0x1d {
			out = append(out, b[i])
			i++

			continue
		}

		if i+1 < len(b) && b[i+1] == '@' {
			i += 2
		} else {
			i += 3
		}
	}

	return out
}

// A guard on the guard: if stripCommands were wrong, the test above would pass
// for the wrong reason.
func TestStripCommands(t *testing.T) {
	in := cat(reset, boldOn, text("ab"), boldOff, underlineOn, text("c"),
		underlineOff, invertOn, text("d"), invertOff, feed(tailFeed))

	if got := stripCommands(in); string(got) != "abcd" {
		t.Errorf("stripCommands = %q, want \"abcd\"", got)
	}
}

// The frame is ours rather than the sender's, so it is asserted separately
// from the golden body tests above.
func TestFrame(t *testing.T) {
	out := Render(doc.Document{Text: "hi"})

	t.Run("title is centred and doubled, then size is restored", func(t *testing.T) {
		want := cat(alignCentre, sizeDouble, text(title+"\n"), sizeNormal)
		if !bytes.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	})

	t.Run("every header and footer line appears", func(t *testing.T) {
		for _, line := range append(append([]string{}, header...), footer...) {
			if !bytes.Contains(out, []byte(line+"\n")) {
				t.Errorf("missing line %q", line)
			}
		}
	})

	// If the body inherited the header's centring, every message would come
	// out centred -- which is us editing what the sender wrote.
	t.Run("body is left aligned", func(t *testing.T) {
		bodyAt := bytes.Index(out, []byte("hi"))
		leftAt := bytes.LastIndex(out[:bodyAt], alignLeft)
		centreAt := bytes.LastIndex(out[:bodyAt], alignCentre)

		if leftAt < centreAt {
			t.Error("body is still centred from the header")
		}
	})

	t.Run("ends left aligned so the next job starts clean", func(t *testing.T) {
		tail := out[bytes.LastIndex(out, []byte(divider())):]
		if !bytes.Contains(tail, alignLeft) {
			t.Error("alignment not reset after the footer")
		}
	})
}

// Overflowing lines wrap on the printer and look like a mistake, and nobody
// checks by eye after editing a string.
func TestFrameFitsThePaper(t *testing.T) {
	if len(title)*2 > cols {
		t.Errorf("title %q is %d columns at double width, over %d", title, len(title)*2, cols)
	}

	for _, line := range append(append([]string{}, header...), footer...) {
		if len(line) > cols {
			t.Errorf("%q is %d columns, over %d", line, len(line), cols)
		}
	}
}

// rasterHeader is GS v 0 for the whole picture: 48 bytes a row, n rows.
func rasterHeader(n int) []byte {
	return []byte{0x1d, 'v', '0', 0, 48, 0, byte(n), byte(n >> 8)}
}

func TestRenderPhotoPacksBits(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, doc.PhotoWidth, 3))
	// White everywhere, then one black pixel top-left and one bottom-right:
	// the first bit of the first byte, and the last bit of the last byte.
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.SetGray(0, 0, color.Gray{0})
	img.SetGray(doc.PhotoWidth-1, 2, color.Gray{0})

	got := body(t, RenderPhoto(img))

	row0 := make([]byte, 48)
	row0[0] = 0x80
	row2 := make([]byte, 48)
	row2[47] = 0x01
	want := cat(rasterHeader(3), row0, make([]byte, 48), row2)

	if !bytes.Equal(got, want) {
		t.Errorf("\ngot  %x\nwant %x", got, want)
	}
}

// Grey is decided at mid-point, so a caller who skipped the dithering still
// gets something rather than a decode error.
func TestRenderPhotoThresholdsGrey(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, doc.PhotoWidth, 1))
	for x := range doc.PhotoWidth {
		img.SetGray(x, 0, color.Gray{uint8(x * 255 / (doc.PhotoWidth - 1))})
	}

	got := body(t, RenderPhoto(img))
	row := got[len(rasterHeader(1)):]

	if row[0] != 0xff || row[47] != 0x00 {
		t.Errorf("gradient row = %x: want black on the left, white on the right", row)
	}
}

// The whole picture is one command, however tall.
func TestRenderPhotoIsOneCommand(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, doc.PhotoWidth, doc.PhotoMaxRows))

	got := body(t, RenderPhoto(img))

	if !bytes.HasPrefix(got, rasterHeader(doc.PhotoMaxRows)) {
		t.Errorf("header = %x, want one raster command of %d rows", got[:8], doc.PhotoMaxRows)
	}

	if want := 8 + 48*doc.PhotoMaxRows; len(got) != want {
		t.Errorf("body is %d bytes, want %d", len(got), want)
	}
}

// A picture receipt is still a FAX receipt: same masthead, same tear-off.
func TestRenderPhotoKeepsTheFrame(t *testing.T) {
	got := RenderPhoto(image.NewGray(image.Rect(0, 0, doc.PhotoWidth, 1)))

	if !bytes.HasPrefix(got, reset) || !bytes.HasSuffix(got, feed(tailFeed)) {
		t.Error("photo receipt is missing the reset or the feed")
	}

	if !bytes.Contains(got, []byte(title)) || !bytes.Contains(got, []byte(footer[0])) {
		t.Error("photo receipt is missing the frame text")
	}
}
