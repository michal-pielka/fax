package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
	"github.com/michal-pielka/fax/server/internal/logging"
)

func textPrint(id string, at int) Print {
	return Print{ID: id, Time: time.Unix(int64(at), 0).UTC(), Kind: "text", Text: "hi " + id}
}

func ids(prints []Print) string {
	var out []string
	for _, p := range prints {
		out = append(out, p.ID)
	}

	return strings.Join(out, ",")
}

func openWall(t *testing.T, dir string) *Wall {
	t.Helper()

	w, err := OpenWall(dir)
	if err != nil {
		t.Fatal(err)
	}

	return w
}

// The wall is the only record of what strangers printed: it has to survive
// a restart, hides included.
func TestWallSurvivesARestart(t *testing.T) {
	dir := t.TempDir()

	w := openWall(t, dir)
	for i, id := range []string{"a", "b", "c"} {
		if err := w.Add(textPrint(id, i)); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := w.Hide("b"); !found || err != nil {
		t.Fatalf("Hide = %v, %v", found, err)
	}

	page, _, _ := openWall(t, dir).Page("", 10)
	if got := ids(page); got != "c,a" {
		t.Errorf("after reopening: %s, want c,a", got)
	}
}

func TestWallPages(t *testing.T) {
	w := openWall(t, "")
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		w.Add(textPrint(id, i))
	}
	w.Hide("c")

	page, more, _ := w.Page("", 2)
	if ids(page) != "e,d" || !more {
		t.Errorf("first page = %s more=%v, want e,d more", ids(page), more)
	}

	// The hidden print is skipped, not counted against the limit.
	page, more, _ = w.Page("d", 2)
	if ids(page) != "b,a" || more {
		t.Errorf("second page = %s more=%v, want b,a and no more", ids(page), more)
	}

	if _, _, ok := w.Page("nope", 2); ok {
		t.Error("an unknown cursor was accepted")
	}
}

func TestWallIgnoresADuplicate(t *testing.T) {
	w := openWall(t, t.TempDir())
	w.Add(textPrint("a", 1))
	w.Add(textPrint("a", 2))

	if page, _, _ := w.Page("", 10); len(page) != 1 {
		t.Errorf("%d prints, want 1", len(page))
	}
}

// A crash mid-write leaves a torn last line; it costs that one print.
func TestWallSkipsATornLine(t *testing.T) {
	dir := t.TempDir()
	good, _ := json.Marshal(textPrint("a", 1))
	os.WriteFile(filepath.Join(dir, printsFile), append(good, []byte("\n{\"id\":\"b\",\"ti")...), 0o644)

	w := openWall(t, dir)
	if page, _, _ := w.Page("", 10); ids(page) != "a" {
		t.Errorf("loaded %s, want a", ids(page))
	}
}

// A viewer that stops reading must not hold up the print that is being
// announced; it is dropped and its channel closed.
func TestWallDropsASlowViewer(t *testing.T) {
	w := openWall(t, "")
	ch, _ := w.Watch()

	for i := range viewerBuffer + 1 {
		w.Add(textPrint(string(rune('a'+i)), i))
	}

	n := 0
	for range ch {
		n++
	}
	if n != viewerBuffer {
		t.Errorf("got %d events before the close, want %d", n, viewerBuffer)
	}
}

func TestDisconnectEndsEveryStream(t *testing.T) {
	w := openWall(t, "")
	ch, _ := w.Watch()

	w.Disconnect()

	if _, open := <-ch; open {
		t.Error("stream still open after Disconnect")
	}
	if _, ok := w.Watch(); ok {
		t.Error("a new viewer was let in while shutting down")
	}
}

func TestOnlyConfirmedPrintsGoOnTheWall(t *testing.T) {
	a := newAPI(&fakeRenderer{payload: []byte("x")}, &fakeDispatcher{})
	body := `{"text":"hello","spans":[{"start":0,"end":5,"style":{"bold":true}}]}`

	rec := do(t, a, http.MethodPost, "/api/print", body)
	var resp struct{ ID string }
	json.NewDecoder(rec.Body).Decode(&resp)

	page, _, _ := a.wall.Page("", 10)
	if len(page) != 1 || page[0].ID != resp.ID || page[0].Text != "hello" || !page[0].Spans[0].Style.Bold {
		t.Fatalf("wall = %+v, want the print with id %s", page, resp.ID)
	}

	failed := newAPI(&fakeRenderer{payload: []byte("x")}, &fakeDispatcher{err: errOffline})
	do(t, failed, http.MethodPost, "/api/print", body)
	if page, _, _ := failed.wall.Page("", 10); len(page) != 0 {
		t.Errorf("an unconfirmed print reached the wall: %+v", page)
	}
}

var errOffline = &upstreamError{Service: "dispatcher", Status: http.StatusServiceUnavailable, Msg: "printer is offline"}

func TestPrintsEndpoint(t *testing.T) {
	a := newAPI(&fakeRenderer{}, &fakeDispatcher{})
	a.wall.Add(textPrint("a", 1))
	a.wall.Add(Print{ID: "b", Kind: "text", Text: "x", Spans: []doc.Span{{Start: 0, End: 1, Style: doc.Style{Invert: true}}}})

	rec := do(t, a, http.MethodGet, "/api/prints?limit=1", "")
	var got struct {
		Prints []Print `json:"prints"`
		More   bool    `json:"more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, %v", rec.Code, err)
	}
	if ids(got.Prints) != "b" || !got.More || !got.Prints[0].Spans[0].Style.Invert {
		t.Errorf("got %+v", got)
	}

	for _, q := range []string{"?limit=0", "?limit=5000", "?limit=x", "?before=nope"} {
		if rec := do(t, a, http.MethodGet, "/api/prints"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, rec.Code)
		}
	}
}

func TestPhotoEndpoint(t *testing.T) {
	a := newAPI(&fakeRenderer{}, &fakeDispatcher{})
	a.photos = t.TempDir()
	os.WriteFile(filepath.Join(a.photos, "p.png"), pngOf(t, doc.PhotoWidth, 2), 0o644)
	a.wall.Add(Print{ID: "p", Kind: "photo", Rows: 2})
	a.wall.Add(textPrint("t", 1))

	rec := do(t, a, http.MethodGet, "/api/prints/p/photo", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}

	for _, id := range []string{"t", "nope", "..%2Fp"} {
		if rec := do(t, a, http.MethodGet, "/api/prints/"+id+"/photo", ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", id, rec.Code)
		}
	}

	a.wall.Hide("p")
	if rec := do(t, a, http.MethodGet, "/api/prints/p/photo", ""); rec.Code != http.StatusNotFound {
		t.Errorf("hidden photo: status %d, want 404", rec.Code)
	}
}

func TestHideEndpoint(t *testing.T) {
	a := newAPI(&fakeRenderer{}, &fakeDispatcher{})
	a.wall.Add(textPrint("a", 1))

	if rec := do(t, a, http.MethodPost, "/internal/prints/a/hide", ""); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if page, _, _ := a.wall.Page("", 10); len(page) != 0 {
		t.Errorf("still on the wall: %+v", page)
	}
	if rec := do(t, a, http.MethodPost, "/internal/prints/nope/hide", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown print: status %d, want 404", rec.Code)
	}
}

// Through a real server: the stream has to outlive the write timeout and
// flush every event as it happens.
func TestStreamSendsPrintsAndHides(t *testing.T) {
	a := newAPI(&fakeRenderer{}, &fakeDispatcher{})
	srv := httptest.NewUnstartedServer(logging.Edge(slog.New(slog.NewTextHandler(io.Discard, nil)))(a.routes()))
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/prints/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	// Past the server's write timeout, which must not apply here.
	time.Sleep(100 * time.Millisecond)
	a.wall.Add(textPrint("a", 1))
	a.wall.Hide("a")

	var got []string
	sc := bufio.NewScanner(resp.Body)
	for len(got) < 4 && sc.Scan() {
		if line := sc.Text(); line != "" {
			got = append(got, line)
		}
	}

	want := []string{"event: print", `data: {"id":"a"`, "event: hide", `data: {"id":"a"}`}
	for i, w := range want {
		if i >= len(got) || !strings.HasPrefix(got[i], w) {
			t.Fatalf("stream = %q, want lines starting %q", got, want)
		}
	}
}
