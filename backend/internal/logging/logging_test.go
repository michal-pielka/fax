package logging

import "testing"

func TestAcceptsValidCombinations(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			if _, err := New(format, level); err != nil {
				t.Errorf("New(%q, %q) = %v", format, level, err)
			}
		}
	}
}

// A typo in a flag should stop the service at startup with a message naming
// the valid values, rather than silently defaulting and hiding logs.
func TestRejectsGarbage(t *testing.T) {
	if _, err := New("yaml", "info"); err == nil {
		t.Error("bad format accepted")
	}

	if _, err := New("json", "verbose"); err == nil {
		t.Error("bad level accepted")
	}
}
