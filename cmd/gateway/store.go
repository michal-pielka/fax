package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/michal-pielka/fax/server/internal/doc"
)

type Status string

const (
	StatusQueued Status = "queued"
	StatusDone   Status = "done"
	StatusFailed Status = "failed"
)

type Job struct {
	ID        string       `json:"id"`
	Status    Status       `json:"status"`
	Document  doc.Document `json:"document"`
	CreatedAt time.Time    `json:"createdAt"`
}

// Store is an in-memory stand-in for the jobs service.
type Store struct {
	mu   sync.Mutex
	jobs map[string]Job
}

func NewStore() *Store {
	return &Store{jobs: make(map[string]Job)}
}

func (s *Store) Add(d doc.Document) Job {
	job := Job{
		ID:        newID(),
		Status:    StatusQueued,
		Document:  d,
		CreatedAt: time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.jobs[job.ID] = job

	return job
}

func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]

	return job, ok
}

// newID returns 16 random bytes as hex. crypto/rand.Read never fails on
// Linux, and the stdlib panics internally if the OS entropy source breaks,
// so there is no error to handle here.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)

	return hex.EncodeToString(b)
}
