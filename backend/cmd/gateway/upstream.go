package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// Renderer and Dispatcher are interfaces so the handlers can be tested without
// either service running.
type Renderer interface {
	Render(ctx context.Context, d doc.Document) ([]byte, error)
}

type Dispatcher interface {
	Print(ctx context.Context, id string, payload []byte) error
	State(ctx context.Context) (State, error)
}

// These three shapes must match what the renderer and dispatcher declare in
// their own packages. They are repeated rather than shared because extracting
// an internal/wire package is a refactor of all three services, not part of
// wiring this one. DisallowUnknownFields on the receiving side turns a drift
// into a loud 400 rather than a silently dropped field.
type renderResponse struct {
	Payload []byte `json:"payload"`
}

type printRequest struct {
	ID      string `json:"id"`
	Payload []byte `json:"payload"`
}

// State mirrors the dispatcher's. The zero value is offline, with no paper and
// nothing printing, so a decode that yields nothing reports the safe answer.
type State struct {
	Online bool `json:"online"`
	Paper  bool `json:"paper"`
	Busy   bool `json:"busy"`
}

// upstreamError keeps the status an internal service replied with. Some of
// those statuses describe the printer rather than a failure, so flattening
// them all into 502 would discard the only information the caller wants.
type upstreamError struct {
	Service string
	Status  int
	Msg     string
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("%s replied %d: %s", e.Service, e.Status, e.Msg)
}

type jsonClient struct {
	name string
	base string
	http *http.Client
}

func (c *jsonClient) post(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}

	return c.do(ctx, http.MethodPost, path, bytes.NewReader(b), out)
}

func (c *jsonClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *jsonClient) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	// The context comes from the inbound handler, so a browser tab that closes
	// mid-print unwinds the whole chain instead of leaving the dispatcher
	// waiting on a broker for a reply nobody will read.
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		var e struct {
			Error string `json:"error"`
		}
		// A broken upstream may reply with anything, so cap the read and
		// ignore a decode failure -- the status is the part that matters.
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)

		return &upstreamError{Service: c.name, Status: resp.StatusCode, Msg: e.Error}
	}

	if out == nil {
		return nil
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

type rendererClient struct{ jsonClient }

func NewRendererClient(base string, hc *http.Client) Renderer {
	return &rendererClient{jsonClient{name: "renderer", base: base, http: hc}}
}

func (c *rendererClient) Render(ctx context.Context, d doc.Document) ([]byte, error) {
	var out renderResponse
	if err := c.post(ctx, "/internal/render", d, &out); err != nil {
		return nil, err
	}

	return out.Payload, nil
}

type dispatcherClient struct{ jsonClient }

func NewDispatcherClient(base string, hc *http.Client) Dispatcher {
	return &dispatcherClient{jsonClient{name: "dispatcher", base: base, http: hc}}
}

func (c *dispatcherClient) Print(ctx context.Context, id string, payload []byte) error {
	return c.post(ctx, "/internal/print", printRequest{ID: id, Payload: payload}, nil)
}

func (c *dispatcherClient) State(ctx context.Context) (State, error) {
	var out State
	err := c.get(ctx, "/internal/state", &out)

	return out, err
}
