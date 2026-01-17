package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimerActivity_Execute(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	timer := NewTimerActivity("test-timer", 1*time.Hour)

	// Create context with fake clock
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	params := make(map[string]any)

	// Start timer in goroutine
	done := make(chan struct{})
	var result any
	var err error
	go func() {
		result, err = timer.Execute(ctx, params)
		close(done)
	}()

	// Timer should not complete yet
	select {
	case <-done:
		t.Fatal("timer completed too early")
	case <-time.After(50 * time.Millisecond):
	}

	// Advance clock past deadline
	clock.Advance(61 * time.Minute)

	// Timer should complete
	select {
	case <-done:
		require.NoError(t, err)
		resultMap, ok := result.(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, resultMap["elapsed"])
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timer did not complete after advancing clock")
	}

	// Check that deadline was stored in params
	_, hasDeadline := params["timer_deadline"]
	require.True(t, hasDeadline, "deadline should be stored in params for checkpointing")
}

func TestTimerActivity_Resume(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	timer := NewTimerActivity("test-timer", 1*time.Hour)

	// Create context with fake clock
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	// Simulate recovery: deadline was set 30 minutes ago
	deadline := start.Add(1 * time.Hour)
	params := map[string]any{
		"timer_deadline": deadline.Format(time.RFC3339Nano),
	}

	// Advance clock to 30 minutes before deadline
	clock.Advance(30 * time.Minute)

	// Start timer in goroutine
	done := make(chan struct{})
	var result any
	var err error
	go func() {
		result, err = timer.Execute(ctx, params)
		close(done)
	}()

	// Timer should not complete yet (30 minutes remaining)
	select {
	case <-done:
		t.Fatal("timer completed too early")
	case <-time.After(50 * time.Millisecond):
	}

	// Advance 31 more minutes past deadline
	clock.Advance(31 * time.Minute)

	// Timer should complete
	select {
	case <-done:
		require.NoError(t, err)
		resultMap, ok := result.(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, resultMap["elapsed"])
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timer did not complete after advancing clock")
	}
}

func TestTimerActivity_AlreadyElapsed(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	timer := NewTimerActivity("test-timer", 1*time.Hour)

	// Create context with fake clock
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	// Simulate recovery where deadline has already passed
	deadline := start.Add(-1 * time.Hour) // 1 hour in the past
	params := map[string]any{
		"timer_deadline": deadline.Format(time.RFC3339Nano),
	}

	// Timer should complete immediately
	result, err := timer.Execute(ctx, params)
	require.NoError(t, err)

	resultMap, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, resultMap["elapsed"])
}

func TestTimerActivity_Cancellation(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	timer := NewTimerActivity("test-timer", 1*time.Hour)

	// Create cancellable context
	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := NewContext(baseCtx, ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	params := make(map[string]any)

	// Start timer in goroutine
	done := make(chan struct{})
	var err error
	go func() {
		_, err = timer.Execute(ctx, params)
		close(done)
	}()

	// Give timer time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Timer should return with context error
	select {
	case <-done:
		require.Error(t, err)
		require.Equal(t, context.Canceled, err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timer did not respond to cancellation")
	}
}

func TestSleepActivity_Execute(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	sleep := NewSleepActivity()

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	params := map[string]any{
		"duration": "1h",
	}

	// Start sleep in goroutine
	done := make(chan struct{})
	var result any
	var err error
	go func() {
		result, err = sleep.Execute(ctx, params)
		close(done)
	}()

	// Should not complete yet
	select {
	case <-done:
		t.Fatal("sleep completed too early")
	case <-time.After(50 * time.Millisecond):
	}

	// Advance clock
	clock.Advance(61 * time.Minute)

	// Should complete
	select {
	case <-done:
		require.NoError(t, err)
		resultMap, ok := result.(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, resultMap["elapsed"])
	case <-time.After(100 * time.Millisecond):
		t.Fatal("sleep did not complete after advancing clock")
	}
}

func TestSleepActivity_InvalidDuration(t *testing.T) {
	clock := NewFakeClock(time.Now())

	sleep := NewSleepActivity()

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	// Missing duration
	_, err := sleep.Execute(ctx, map[string]any{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")

	// Invalid duration string
	_, err = sleep.Execute(ctx, map[string]any{"duration": "invalid"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid")
}
