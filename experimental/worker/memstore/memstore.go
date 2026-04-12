// Package memstore provides an in-memory implementation of
// worker.QueueStore suitable for tests and local development.
//
// It is not goroutine-safe across processes (there is no process!)
// but handles concurrent access from multiple goroutines within one
// process correctly.
package memstore

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// Store is an in-memory worker.QueueStore.
type Store struct {
	now func() time.Time

	mu   sync.Mutex
	runs map[string]*runRow
}

type runRow struct {
	id           string
	spec         []byte
	status       worker.Status
	attempt      int
	claimedBy    string
	heartbeatAt  time.Time
	result       []byte
	errorMessage string
	createdAt    time.Time
	startedAt    time.Time
	completedAt  time.Time
	orgID        string
	workflowType string
	creditCost   int
	callbackURL  string
}

// New constructs an empty Store.
func New() *Store {
	return &Store{
		now:  time.Now,
		runs: make(map[string]*runRow),
	}
}

// SetClock overrides the time source. Tests only.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Len returns the number of runs in the store.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

// Snapshot returns a copy of all runs keyed by ID. Tests only.
func (s *Store) Snapshot() map[string]Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Run, len(s.runs))
	for id, row := range s.runs {
		out[id] = Run{
			ID:           row.id,
			Status:       row.status,
			Attempt:      row.attempt,
			ClaimedBy:    row.claimedBy,
			Result:       append([]byte(nil), row.result...),
			ErrorMessage: row.errorMessage,
		}
	}
	return out
}

// Run is a read-only snapshot of a run. Returned by Snapshot for
// tests to inspect state.
type Run struct {
	ID           string
	Status       worker.Status
	Attempt      int
	ClaimedBy    string
	Result       []byte
	ErrorMessage string
}

// Enqueue implements worker.QueueStore.
func (s *Store) Enqueue(_ context.Context, run worker.NewRun) error {
	if run.ID == "" {
		return fmt.Errorf("memstore: NewRun.ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[run.ID]; exists {
		return fmt.Errorf("memstore: run %s already exists", run.ID)
	}
	s.runs[run.ID] = &runRow{
		id:           run.ID,
		spec:         append([]byte(nil), run.Spec...),
		status:       worker.StatusQueued,
		createdAt:    s.now(),
		orgID:        run.OrgID,
		workflowType: run.WorkflowType,
		creditCost:   run.CreditCost,
		callbackURL:  run.CallbackURL,
	}
	return nil
}

// ClaimQueued implements worker.QueueStore.
func (s *Store) ClaimQueued(_ context.Context, workerID string) (*worker.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var candidates []*runRow
	for _, row := range s.runs {
		if row.status == worker.StatusQueued {
			candidates = append(candidates, row)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].createdAt.Equal(candidates[j].createdAt) {
			return candidates[i].createdAt.Before(candidates[j].createdAt)
		}
		return candidates[i].id < candidates[j].id
	})

	row := candidates[0]
	now := s.now()
	row.status = worker.StatusRunning
	row.claimedBy = workerID
	row.heartbeatAt = now
	row.startedAt = now
	row.attempt++

	return &worker.Claim{
		ID:           row.id,
		Spec:         append([]byte(nil), row.spec...),
		Attempt:      row.attempt,
		OrgID:        row.orgID,
		WorkflowType: row.workflowType,
		CreditCost:   row.creditCost,
		CallbackURL:  row.callbackURL,
	}, nil
}

// Heartbeat implements worker.QueueStore.
func (s *Store) Heartbeat(_ context.Context, lease worker.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.runs[lease.RunID]
	if !ok {
		return worker.ErrLeaseLost
	}
	if row.status != worker.StatusRunning ||
		row.claimedBy != lease.WorkerID ||
		row.attempt != lease.Attempt {
		return worker.ErrLeaseLost
	}
	row.heartbeatAt = s.now()
	return nil
}

// Complete implements worker.QueueStore.
func (s *Store) Complete(_ context.Context, lease worker.Lease, outcome worker.Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.runs[lease.RunID]
	if !ok {
		return worker.ErrLeaseLost
	}
	if row.claimedBy != lease.WorkerID || row.attempt != lease.Attempt {
		return worker.ErrLeaseLost
	}
	row.status = outcome.Status
	row.result = append([]byte(nil), outcome.Result...)
	row.errorMessage = outcome.ErrorMessage
	if outcome.Status == worker.StatusCompleted ||
		outcome.Status == worker.StatusFailed {
		row.completedAt = s.now()
	}
	return nil
}

// ReclaimStale implements worker.QueueStore.
func (s *Store) ReclaimStale(_ context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	excluded := toSet(excludeIDs)
	reclaimed := 0
	for _, row := range s.runs {
		if row.status != worker.StatusRunning {
			continue
		}
		if !row.heartbeatAt.Before(staleBefore) {
			continue
		}
		if row.attempt >= maxAttempts {
			continue
		}
		if _, skip := excluded[row.id]; skip {
			continue
		}
		row.status = worker.StatusQueued
		row.claimedBy = ""
		row.heartbeatAt = time.Time{}
		row.startedAt = time.Time{}
		reclaimed++
	}
	return reclaimed, nil
}

// DeadLetterStale implements worker.QueueStore.
func (s *Store) DeadLetterStale(_ context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	excluded := toSet(excludeIDs)
	var ids []string
	now := s.now()
	for _, row := range s.runs {
		if row.status != worker.StatusRunning {
			continue
		}
		if !row.heartbeatAt.Before(staleBefore) {
			continue
		}
		if row.attempt < maxAttempts {
			continue
		}
		if _, skip := excluded[row.id]; skip {
			continue
		}
		row.status = worker.StatusFailed
		row.errorMessage = fmt.Sprintf("exceeded max retry attempts (%d)", maxAttempts)
		row.claimedBy = ""
		row.heartbeatAt = time.Time{}
		row.completedAt = now
		ids = append(ids, row.id)
	}
	slices.Sort(ids)
	return ids, nil
}

func toSet(s []string) map[string]struct{} {
	m := make(map[string]struct{}, len(s))
	for _, v := range s {
		m[v] = struct{}{}
	}
	return m
}
