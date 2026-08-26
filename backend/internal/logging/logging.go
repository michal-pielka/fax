// Package logging builds the logger each service uses, so the three of them
// agree on format and level without repeating the setup.
package logging

import (
	"fmt"
	"log/slog"
	"os"
)

// New returns a logger writing to stdout, where Docker collects it.
//
// JSON is the default because these logs are read through `docker compose
// logs`, and structured output is what makes them answerable with jq rather
// than grep. Text stays available for reading them by eye during development.
func New(format, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}

	opts := &slog.HandlerOptions{Level: lvl}

	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stdout, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stdout, opts)), nil
	default:
		return nil, fmt.Errorf("log format %q: want json or text", format)
	}
}
