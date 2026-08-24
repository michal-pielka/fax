package render

import (
	"bytes"
	"testing"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// cat joins byte sequences, so an expected receipt reads as the sequence of
// commands it actually is rather than one opaque literal.
func cat(parts ...[]byte) []byte {
	return bytes.Join(parts, nil)
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
			want: cat(reset, text("hi"), feed(tailFeed)),
		},
		{
			name: "newlines pass straight through",
			in:   doc.Document{Text: "one\ntwo"},
			want: cat(reset, text("one\ntwo"), feed(tailFeed)),
		},
		{
			name: "bold span toggles on and off",
			in:   doc.Document{Text: "hi", Spans: []doc.Span{bold(0, 1)}},
			want: cat(reset, boldOn, text("h"), boldOff, text("i"), feed(tailFeed)),
		},
		{
			name: "bold running to the end is closed before the feed",
			in:   doc.Document{Text: "hi", Spans: []doc.Span{bold(0, 2)}},
			want: cat(reset, boldOn, text("hi"), boldOff, feed(tailFeed)),
		},
		{
			name: "underline",
			in: doc.Document{Text: "ab", Spans: []doc.Span{
				{Start: 1, End: 2, Style: doc.Style{Underline: true}},
			}},
			want: cat(reset, text("a"), underlineOn, text("b"), underlineOff, feed(tailFeed)),
		},
		{
			name: "bold and underline together emit both",
			in: doc.Document{Text: "x", Spans: []doc.Span{
				{Start: 0, End: 1, Style: doc.Style{Bold: true, Underline: true}},
			}},
			want: cat(reset, boldOn, underlineOn, text("x"), boldOff, underlineOff, feed(tailFeed)),
		},
		{
			// The case that breaks a boundary-walking implementation: it would
			// switch bold off at offset 3 where the first span ends, even
			// though the second still wants it on.
			name: "overlapping spans do not switch off at the join",
			in:   doc.Document{Text: "abcdef", Spans: []doc.Span{bold(0, 3), bold(2, 5)}},
			want: cat(reset, boldOn, text("abcde"), boldOff, text("f"), feed(tailFeed)),
		},
		{
			name: "span order does not matter",
			in:   doc.Document{Text: "abcdef", Spans: []doc.Span{bold(4, 6), bold(0, 2)}},
			want: cat(reset, boldOn, text("ab"), boldOff, text("cd"),
				boldOn, text("ef"), boldOff, feed(tailFeed)),
		},
		{
			name: "adjacent spans do not emit a redundant toggle",
			in:   doc.Document{Text: "abcd", Spans: []doc.Span{bold(0, 2), bold(2, 4)}},
			want: cat(reset, boldOn, text("abcd"), boldOff, feed(tailFeed)),
		},
		{
			name: "a span covering nothing changes nothing",
			in:   doc.Document{Text: "ab", Spans: nil},
			want: cat(reset, text("ab"), feed(tailFeed)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render(tt.in)
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
		Start: 2, End: 5, Style: doc.Style{Underline: true},
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
	const body = "Order #1234\nTotal: $9.99\n** thanks **"

	got := Render(doc.Document{Text: body, Spans: []doc.Span{bold(0, 5)}})

	if stripped := stripCommands(got); !bytes.Equal(stripped, []byte(body)) {
		t.Errorf("text was altered:\ngot  %q\nwant %q", stripped, body)
	}
}

// stripCommands removes the ESC sequences this package emits, leaving only
// what lands on paper. ESC @ is two bytes; the rest take a parameter and are
// three.
func stripCommands(b []byte) []byte {
	var out []byte

	for i := 0; i < len(b); {
		if b[i] != 0x1b {
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
		underlineOff, feed(tailFeed))

	if got := stripCommands(in); string(got) != "abc" {
		t.Errorf("stripCommands = %q, want \"abc\"", got)
	}
}
