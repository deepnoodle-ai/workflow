package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

const (
	chain = `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x", "next": ["c"]},
	  {"name": "c", "action": "x"}]}`
	napFirst = `{"steps": [
	  {"name": "nap", "sleep": "1h", "next": ["a"]},
	  {"name": "a", "action": "x"}]}`
)

// counting returns handlers that count their calls by run and return "ok".
func counting() (map[string]workflow.Handler, func(runID string) int) {
	var mu sync.Mutex
	calls := map[string]int{}
	h := func(_ context.Context, job workflow.Job) (any, error) {
		mu.Lock()
		calls[job.RunID]++
		mu.Unlock()
		return "ok", nil
	}
	return map[string]workflow.Handler{"action": h}, func(runID string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[runID]
	}
}

func runStatus(t *testing.T, s *Store, runID string) workflow.RunStatus {
	t.Helper()
	r, err := s.GetRun(context.Background(), runID)
	require.NoError(t, err)
	return r.Status
}

// runOnceWithin runs RunOnce and fails the test if it has not returned
// within d.
func runOnceWithin(t *testing.T, w *Worker, ctx context.Context, d time.Duration) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- w.RunOnce(ctx) }()
	select {
	case err := <-result:
		return err
	case <-time.After(d):
		t.Fatalf("RunOnce did not return within %s", d)
		return nil
	}
}

// TestRunOnceReturnsAClaimError: a claim that fails ends the call with
// its error.
func TestRunOnceReturnsAClaimError(t *testing.T) {
	s := newStore(t)
	missing, err := New(s.pool, kinds, WithSchema(s.schema+"_missing")) // never migrated
	require.NoError(t, err)
	handlers, _ := counting()
	w := &Worker{Store: missing, Handlers: handlers, Logger: slog.New(slog.DiscardHandler)}

	err = runOnceWithin(t, w, context.Background(), 10*time.Second)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "does not exist"), err.Error())
}

// TestRunOnceDoesNotListen: RunOnce opens no LISTEN connection, so a
// ListenConfig that cannot connect changes nothing.
func TestRunOnceDoesNotListen(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "r")
	cfg, err := pgx.ParseConfig("postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	require.NoError(t, err)
	var logs syncBuffer
	handlers, _ := counting()
	w := &Worker{
		Store:        s,
		Handlers:     handlers,
		ListenConfig: cfg,
		Logger:       slog.New(slog.NewTextHandler(&logs, nil)),
	}

	require.NoError(t, runOnceWithin(t, w, context.Background(), 10*time.Second))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
	require.False(t, strings.Contains(logs.String(), "listen"), logs.String())
}

// breakDefinitions returns a store over s's tables whose cache holds only
// the definitions of keep, and makes loading any other definition fail
// with a database error (not ErrDefinitionUnavailable).
func breakDefinitions(t *testing.T, s *Store, keep ...string) *Store {
	t.Helper()
	ctx := context.Background()
	fresh, err := New(s.pool, kinds, WithSchema(s.schema))
	require.NoError(t, err)
	for _, runID := range keep {
		r, err := s.GetRun(ctx, runID)
		require.NoError(t, err)
		_, err = fresh.core.Definition(ctx, fresh.backend, r.DefinitionHash)
		require.NoError(t, err)
	}
	_, err = s.pool.Exec(ctx,
		s.sql(`ALTER TABLE {{schema}}.definitions RENAME COLUMN document TO broken`))
	require.NoError(t, err)
	return fresh
}

// TestRunOnceReportsADefinitionLoadError: a job whose definition cannot be
// read for a database error stays under its lease; the other due job still
// runs, and RunOnce returns the load error.
func TestRunOnceReportsADefinitionLoadError(t *testing.T) {
	s := newStore(t)
	startRuns(t, s, oneStep, "good")
	startRuns(t, s, `{"steps": [{"name": "b", "action": "x"}]}`, "bad")
	fresh := breakDefinitions(t, s, "good")
	handlers, calls := counting()
	w := &Worker{
		Store:       fresh,
		Handlers:    handlers,
		Concurrency: 1,
		Logger:      slog.New(slog.DiscardHandler),
	}

	err := runOnceWithin(t, w, context.Background(), 10*time.Second)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDefinitionUnavailable), err.Error())
	require.True(t, strings.Contains(err.Error(), "load definition"), err.Error())
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "good"))
	require.Equal(t, 0, calls("bad"))
	claims, leased := soleJob(t, s, "bad")
	require.Equal(t, 1, claims)
	require.True(t, leased, "left under its lease")
}
