package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// The wall's files. Both are append-only, one record per line, so a crash
// can at worst leave a torn last line, which loading skips.
const (
	printsFile = "prints.jsonl"
	hiddenFile = "hidden.txt"
)

const (
	// maxViewers caps open live streams: each holds a connection and a
	// goroutine, and nothing else stops a crowd holding thousands.
	maxViewers = 500
	// viewerBuffer is how far a viewer may fall behind before it is dropped.
	// Its browser reconnects and refetches, so nothing is lost for good.
	viewerBuffer = 16
)

// Print is one receipt on the wall: what was sent, not the bytes it became.
// Never the sender's address.
type Print struct {
	ID    string     `json:"id"`
	Time  time.Time  `json:"time"`
	Kind  string     `json:"kind"` // "text" or "photo"
	Text  string     `json:"text,omitempty"`
	Spans []doc.Span `json:"spans,omitempty"`
	Rows  int        `json:"rows,omitempty"`
}

// event is one message for the live stream, encoded once for every viewer.
type event struct {
	name string
	data []byte
}

// Wall is every print, in the order they came out. It lives in memory and,
// given a directory, on disk: a year of receipts is a few megabytes.
type Wall struct {
	mu      sync.Mutex
	prints  []Print        // oldest first
	index   map[string]int // id to position in prints
	hidden  map[string]bool
	file    *os.File // nil when kept in memory only
	hides   *os.File
	viewers map[chan event]struct{}
	closed  bool
}

// OpenWall loads the wall kept in dir, creating it if need be. An empty dir
// keeps the wall in memory only, which is what tests and a laptop want.
func OpenWall(dir string) (*Wall, error) {
	w := &Wall{
		index:   map[string]int{},
		hidden:  map[string]bool{},
		viewers: map[chan event]struct{}{},
	}
	if dir == "" {
		return w, nil
	}

	prints, err := readPrints(filepath.Join(dir, printsFile))
	if err != nil {
		return nil, err
	}
	for _, p := range prints {
		w.insert(p)
	}

	ids, err := readLines(filepath.Join(dir, hiddenFile))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		w.hidden[id] = true
	}

	if w.file, err = openAppend(filepath.Join(dir, printsFile)); err != nil {
		return nil, err
	}
	if w.hides, err = openAppend(filepath.Join(dir, hiddenFile)); err != nil {
		return nil, err
	}

	return w, nil
}

// Add records a print and tells every viewer. A print already on the wall is
// ignored, so a retried write cannot show one receipt twice.
func (w *Wall) Add(p Print) error {
	line, err := json.Marshal(p)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, dup := w.index[p.ID]; dup {
		return nil
	}
	if w.file != nil {
		if _, err := w.file.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("record print: %w", err)
		}
	}

	w.insert(p)
	w.broadcast(event{"print", line})

	return nil
}

// Hide takes a print off the wall for good. False if there is no such print.
func (w *Wall) Hide(id string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.index[id]; !ok {
		return false, nil
	}
	if w.hidden[id] {
		return true, nil
	}
	if w.hides != nil {
		if _, err := w.hides.WriteString(id + "\n"); err != nil {
			return true, fmt.Errorf("record hide: %w", err)
		}
	}

	w.hidden[id] = true
	data, _ := json.Marshal(map[string]string{"id": id})
	w.broadcast(event{"hide", data})

	return true, nil
}

// Page returns up to limit visible prints older than the one with id before,
// newest first; an empty before starts at the newest. more says whether
// older ones remain. ok is false for a before that names no print.
func (w *Wall) Page(before string, limit int) (page []Print, more, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	i := len(w.prints)
	if before != "" {
		if i, ok = w.index[before]; !ok {
			return nil, false, false
		}
	}

	page = []Print{}
	for i--; i >= 0 && len(page) < limit; i-- {
		if p := w.prints[i]; !w.hidden[p.ID] {
			page = append(page, p)
		}
	}

	return page, i >= 0, true
}

// Get returns a visible print.
func (w *Wall) Get(id string) (Print, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	i, ok := w.index[id]
	if !ok || w.hidden[id] {
		return Print{}, false
	}

	return w.prints[i], true
}

// Watch returns a channel of everything that happens to the wall from now
// on, or false if the wall is full of viewers or shutting down. The channel
// closes when the viewer is dropped; call Unwatch when done with it.
func (w *Wall) Watch() (chan event, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed || len(w.viewers) >= maxViewers {
		return nil, false
	}

	ch := make(chan event, viewerBuffer)
	w.viewers[ch] = struct{}{}

	return ch, true
}

func (w *Wall) Unwatch(ch chan event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.drop(ch)
}

// Disconnect ends every live stream and refuses new ones. For shutdown:
// http.Server.Shutdown waits for handlers, and a stream never returns alone.
func (w *Wall) Disconnect() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closed = true
	for ch := range w.viewers {
		w.drop(ch)
	}
}

func (w *Wall) insert(p Print) {
	if _, dup := w.index[p.ID]; dup {
		return
	}

	w.index[p.ID] = len(w.prints)
	w.prints = append(w.prints, p)
}

// broadcast never blocks the print that caused it: a viewer too slow to take
// the event is dropped instead.
func (w *Wall) broadcast(ev event) {
	for ch := range w.viewers {
		select {
		case ch <- ev:
		default:
			w.drop(ch)
		}
	}
}

func (w *Wall) drop(ch chan event) {
	if _, ok := w.viewers[ch]; ok {
		delete(w.viewers, ch)
		close(ch)
	}
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}

// readPrints loads a prints file, skipping lines that do not parse: a torn
// last line from a crash should cost one receipt, not the wall.
func readPrints(path string) ([]Print, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var prints []Print
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var p Print
		if json.Unmarshal(sc.Bytes(), &p) == nil && p.ID != "" {
			prints = append(prints, p)
		}
	}

	return prints, sc.Err()
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var lines []string
	for _, l := range bytes.Split(b, []byte("\n")) {
		if l = bytes.TrimSpace(l); len(l) > 0 {
			lines = append(lines, string(l))
		}
	}

	return lines, nil
}
