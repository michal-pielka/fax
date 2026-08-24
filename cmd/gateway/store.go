package main

import (
	"sync"
	"time"

	"github.com/google/uuid"

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
		ID:        uuid.NewString(),
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
