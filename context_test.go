package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestContext_Now(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		Clock:          clock,
	})

	assert.Equal(t, ctx.Now(), start)

	clock.Advance(1 * time.Hour)
	assert.Equal(t, ctx.Now(), start.Add(1*time.Hour))
}

func TestContext_GetExecutionID(t *testing.T) {
	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
	})

	assert.Equal(t, ctx.GetExecutionID(), "exec-123")
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
	assert.NotEqual(t, id1, id2)

	// IDs should have the correct prefix
	assert.Contains(t, id1, "order_")
	assert.Contains(t, id2, "order_")
	assert.Contains(t, id3, "user_")

	// Create a new context with same parameters - should produce same sequence
	ctx2 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-123",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	id1_again := ctx2.DeterministicID("order")
	assert.Equal(t, id1_again, id1, "same inputs should produce same ID")

	// Different execution ID should produce different IDs
	ctx3 := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: NewPathLocalState(map[string]any{}, map[string]any{}),
		ExecutionID:    "exec-456",
		PathID:         "path-main",
		StepName:       "step-1",
	})

	id_different := ctx3.DeterministicID("order")
	assert.NotEqual(t, id1, id_different, "different execution should produce different ID")
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
	assert.Equal(t, r2.Intn(1000), val1, "same inputs should produce same random sequence")
	assert.Equal(t, r2.Intn(1000), val2)
	assert.Equal(t, r2.Intn(1000), val3)

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
		assert.NotEqual(t, differentVal2, sameVal2, "different executions should produce different sequences")
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

	assert.True(t, r1 == r2, "Rand() should return same instance")
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

	assert.Equal(t, child.GetExecutionID(), "exec-123")
	assert.Equal(t, child.GetPathID(), "path-main")
	assert.Equal(t, child.GetStepName(), "step-1")
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

	assert.Equal(t, child.GetExecutionID(), "exec-123")
	assert.Equal(t, child.GetPathID(), "path-main")
	assert.Equal(t, child.GetStepName(), "step-1")
}
