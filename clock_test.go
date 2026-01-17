package workflow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRealClock_Now(t *testing.T) {
	clock := NewRealClock()

	before := time.Now()
	now := clock.Now()
	after := time.Now()

	require.True(t, !now.Before(before), "clock.Now() should not be before time.Now()")
	require.True(t, !now.After(after), "clock.Now() should not be after time.Now()")
}

func TestRealClock_After(t *testing.T) {
	clock := NewRealClock()

	start := time.Now()
	ch := clock.After(10 * time.Millisecond)

	select {
	case <-ch:
		elapsed := time.Since(start)
		require.True(t, elapsed >= 10*time.Millisecond, "should wait at least 10ms")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("clock.After did not fire within 100ms")
	}
}

func TestFakeClock_Now(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	require.Equal(t, start, clock.Now())

	clock.Advance(1 * time.Hour)
	require.Equal(t, start.Add(1*time.Hour), clock.Now())
}

func TestFakeClock_After(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	ch := clock.After(1 * time.Hour)

	// Should not fire yet
	select {
	case <-ch:
		t.Fatal("clock.After fired too early")
	default:
	}

	// Advance partway
	clock.Advance(30 * time.Minute)
	select {
	case <-ch:
		t.Fatal("clock.After fired too early")
	default:
	}

	// Advance past deadline
	clock.Advance(31 * time.Minute)
	select {
	case received := <-ch:
		require.Equal(t, start.Add(61*time.Minute), received)
	default:
		t.Fatal("clock.After did not fire after advancing past deadline")
	}
}

func TestFakeClock_After_AlreadyPast(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	// Zero duration should fire immediately
	ch := clock.After(0)
	select {
	case <-ch:
		// Expected
	default:
		t.Fatal("clock.After(0) should fire immediately")
	}
}

func TestFakeClock_After_Multiple(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	ch1 := clock.After(10 * time.Minute)
	ch2 := clock.After(20 * time.Minute)
	ch3 := clock.After(30 * time.Minute)

	require.Equal(t, 3, clock.WaiterCount())

	// Advance past first
	clock.Advance(15 * time.Minute)
	require.Equal(t, 2, clock.WaiterCount())
	select {
	case <-ch1:
	default:
		t.Fatal("ch1 should have fired")
	}

	// Advance past all
	clock.Advance(20 * time.Minute)
	require.Equal(t, 0, clock.WaiterCount())
	select {
	case <-ch2:
	default:
		t.Fatal("ch2 should have fired")
	}
	select {
	case <-ch3:
	default:
		t.Fatal("ch3 should have fired")
	}
}

func TestFakeClock_Set(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)

	ch := clock.After(1 * time.Hour)

	// Set to a time after the deadline
	future := start.Add(2 * time.Hour)
	clock.Set(future)

	require.Equal(t, future, clock.Now())
	select {
	case <-ch:
		// Expected
	default:
		t.Fatal("clock.After should have fired after Set")
	}
}
