package doc

// The paper, as both ends see it. The frontend lays text out to these and the
// server enforces them; the server's word is the one that counts.
const (
	// Cols is Font A across the 384 printable dots: 12 dots per character.
	Cols = 32
	// Rows is how many lines the preview shows, and so how long a receipt's
	// body may be. A line longer than Cols wraps on the printer and costs more.
	Rows = 9

	// PhotoWidth is the printable width in dots. A photo is exactly this wide:
	// the browser scales it, the server only checks.
	PhotoWidth = 384
	// PhotoMaxRows caps a photo's height in dots. A square: 48 mm of paper,
	// about 18 KB of raster, nineteen seconds on the wire at 9600 baud.
	PhotoMaxRows = 384
)

// Limits is the paper a document must fit. A struct rather than the constants
// so tests can print on a smaller sheet.
type Limits struct {
	Cols int
	Rows int
}

// Paper is the real one.
var Paper = Limits{Cols: Cols, Rows: Rows}

// Document is a text receipt: the words, and the ranges that are styled.
type Document struct {
	Text  string `json:"text"`
	Spans []Span `json:"spans,omitempty"`
}

type Span struct {
	Start int   `json:"start"`
	End   int   `json:"end"`
	Style Style `json:"style"`
}

// Style is what the printer can do to a character without changing its size:
// the frame's columns stay put whatever is switched on.
type Style struct {
	Bold bool `json:"bold,omitempty"`
	// Underline is the printer's two-dot underline; the one-dot one is too
	// faint on thermal paper to be worth offering.
	Underline bool `json:"underline,omitempty"`
	// Invert prints white on black: the whole character cell goes dark.
	Invert bool `json:"invert,omitempty"`
}
