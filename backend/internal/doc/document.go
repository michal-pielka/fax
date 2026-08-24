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

type Style struct {
	Bold      bool `json:"bold,omitempty"`
	Underline bool `json:"underline,omitempty"`
}
