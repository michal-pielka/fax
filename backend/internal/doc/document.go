package doc

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
