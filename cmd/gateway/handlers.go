package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/michal-pielka/fax/server/internal/doc"
)

// maxBody caps the request body before the JSON decoder sees it.
// Validate protects against a large document;
// only this protects against a body that never stops arriving.
const maxBody = 64 << 10 // 64 KiB

type api struct {
	store  *Store
	limits doc.Limits
	log    *slog.Logger
}

func (a *api) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/jobs", a.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", a.getJob)
	mux.HandleFunc("GET /api/health", a.health)

	return mux
}

func (a *api) createJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	var d doc.Document
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a typo'd field is a 400, not silent data loss

	if err := dec.Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}

	if err := d.Validate(a.limits); err != nil {
		if errors.Is(err, doc.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.log.Error("validate", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	job := a.store.Add(d)
	a.log.Info("job accepted", "id", job.ID, "runes", len(d.Text))

	w.Header().Set("Location", "/api/jobs/"+job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (a *api) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := a.store.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
