package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContext_Now(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	require.Equal(t, start, ctx.Now())

	clock.Advance(1 * time.Hour)
	require.Equal(t, start.Add(1*time.Hour), ctx.Now())
}

func TestContext_GetExecutionID(t *testing.T) {
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
	})

	require.Equal(t, "exec-123", ctx.GetExecutionID())
}

func TestContext_DeterministicID(t *testing.T) {
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	// Generate IDs - they should be deterministic given the same inputs
	id1 := ctx.DeterministicID("order")
	id2 := ctx.DeterministicID("order")
	id3 := ctx.DeterministicID("user")

	// Each call should produce a different ID (counter increments)
	require.NotEqual(t, id1, id2)

	// IDs should have the correct prefix
	require.Contains(t, id1, "order_")
	require.Contains(t, id2, "order_")
	require.Contains(t, id3, "user_")

	// Create a new context with same parameters - should produce same sequence
	ctx2 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	id1_again := ctx2.DeterministicID("order")
	require.Equal(t, id1, id1_again, "same inputs should produce same ID")

	// Different execution ID should produce different IDs
	ctx3 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-456",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	id_different := ctx3.DeterministicID("order")
	require.NotEqual(t, id1, id_different, "different execution should produce different ID")
}

func TestContext_Rand(t *testing.T) {
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
	})

	// Get random values
	r := ctx.Rand()
	val1 := r.Intn(1000)
	val2 := r.Intn(1000)
	val3 := r.Intn(1000)

	// Create a new context with same parameters - should produce same sequence
	ctx2 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
	})

	r2 := ctx2.Rand()
	require.Equal(t, val1, r2.Intn(1000), "same inputs should produce same random sequence")
	require.Equal(t, val2, r2.Intn(1000))
	require.Equal(t, val3, r2.Intn(1000))

	// Different execution ID should produce different sequence
	ctx3 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-456",
		PathID:         "path-main",
	})

	r3 := ctx3.Rand()
	// Very unlikely to produce the same first value with different seed
	differentVal := r3.Intn(1000)
	// Note: there's a tiny chance this could fail due to random collision
	// We reset r2 by creating a new context to ensure fair comparison
	ctx4 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
	})
	r4 := ctx4.Rand()
	sameVal := r4.Intn(1000)

	// Check that different execution IDs produce different sequences
	// There's a 1/1000 chance of collision, so we test multiple values if needed
	if differentVal == sameVal {
		differentVal2 := r3.Intn(1000)
		sameVal2 := r4.Intn(1000)
		require.NotEqual(t, differentVal2, sameVal2, "different executions should produce different sequences")
	}
}

func TestContext_Rand_SameInstance(t *testing.T) {
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
	})

	// Multiple calls to Rand() should return the same instance
	r1 := ctx.Rand()
	r2 := ctx.Rand()

	require.Same(t, r1, r2, "Rand() should return same instance")
}

func TestWithTimeout_PreservesExecutionID(t *testing.T) {
	parent := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	child, cancel := WithTimeout(parent, 1*time.Second)
	defer cancel()

	require.Equal(t, "exec-123", child.GetExecutionID())
	require.Equal(t, "path-main", child.GetPathID())
	require.Equal(t, "step-1", child.GetStepName())
}

func TestWithCancel_PreservesExecutionID(t *testing.T) {
	parent := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	child, cancel := WithCancel(parent)
	defer cancel()

	require.Equal(t, "exec-123", child.GetExecutionID())
	require.Equal(t, "path-main", child.GetPathID())
	require.Equal(t, "step-1", child.GetStepName())
}
