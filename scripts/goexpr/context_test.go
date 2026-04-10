package goexpr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRun_CancelledBeforeRun verifies that an already-cancelled context
// is observed at the very first eval step and the evaluator exits
// without walking the AST.
func TestRun_CancelledBeforeRun(t *testing.T) {
	p, err := Compile("1 + 2 * 3 - 4")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = p.Run(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}

// TestRun_CancelMidEvaluation installs a registered function that
// cancels the context when called, then evaluates an expression whose
// right-hand side would execute *after* the cancelling call. The next
// eval tick must observe ctx.Err() and abort, even though no Go-level
// blocking happens.
func TestRun_CancelMidEvaluation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e := New(WithFunctions(map[string]any{
		"stop": func() bool { cancel(); return true },
	}))

	// `+` is not short-circuited, so the right-hand eval call must run
	// after stop() returns — and it's where the ctx.Err() check fires.
	_, err := e.Eval(ctx, "stop() && 1 < 2", nil)
	require.ErrorIs(t, err, context.Canceled)
}

// TestRun_DeadlineExceeded mirrors the cancellation path but uses a
// context.DeadlineExceeded error so callers can distinguish timeouts.
func TestRun_DeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	// Busy-wait one tick so the deadline is guaranteed past.
	time.Sleep(time.Millisecond)

	_, err := Eval(ctx, "1 + 2", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestRun_NilContextDefaults verifies that passing a nil context to Run
// falls back to context.Background instead of panicking.
func TestRun_NilContextDefaults(t *testing.T) {
	p, err := Compile("40 + 2")
	require.NoError(t, err)
	//nolint:staticcheck // intentionally passing nil to verify fallback
	got, err := p.Run(nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

// TestCtxInjection_ZeroArgFunc verifies that a function declared as
// `func(ctx) T` is called with the live context and zero user args.
func TestCtxInjection_ZeroArgFunc(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")

	var seen any
	e := New(WithFunctions(map[string]any{
		"probe": func(c context.Context) string {
			seen = c.Value(key{})
			return "ok"
		},
	}))
	v, err := e.Eval(ctx, "probe()", nil)
	require.NoError(t, err)
	require.Equal(t, "ok", v)
	require.Equal(t, "marker", seen)
}

// TestCtxInjection_WithArgs checks that user arguments after the ctx
// parameter are matched positionally and numeric coercion still applies.
// The user function returns int, and goexpr does not re-coerce return
// values — the concrete Go type is preserved to the caller.
func TestCtxInjection_WithArgs(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"add": func(_ context.Context, a, b int) int { return a + b },
	}))
	v, err := e.Eval(context.Background(), "add(2, 3)", nil)
	require.NoError(t, err)
	require.Equal(t, int(5), v)
}

// TestCtxInjection_ArgCountErrorExcludesCtx verifies the arity error
// reports the user-visible parameter count, not the raw ft.NumIn().
// A caller who passes one arg too few should see "expects 2 args" for a
// `func(ctx, a, b)` signature.
func TestCtxInjection_ArgCountErrorExcludesCtx(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"add": func(_ context.Context, a, b int) int { return a + b },
	}))
	_, err := e.Eval(context.Background(), "add(1)", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "expects 2 args")
}

// TestCtxInjection_Variadic verifies auto-injection composes with
// variadic tails: `func(ctx, prefix, vals...)` works from the expression
// `tag("k", 1, 2, 3)`.
func TestCtxInjection_Variadic(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"sum": func(_ context.Context, prefix string, xs ...int) string {
			total := 0
			for _, x := range xs {
				total += x
			}
			return prefix
		},
	}))
	v, err := e.Eval(context.Background(), `sum("k", 1, 2, 3)`, nil)
	require.NoError(t, err)
	require.Equal(t, "k", v)
}

// TestCtxInjection_NotFirstParam verifies that context.Context is only
// injected when it is the *first* parameter. A function that takes ctx
// in any other position should be treated as an ordinary arg (and fail
// with a clear type error when expression args can't satisfy it).
func TestCtxInjection_NotFirstParam(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"weird": func(a int, _ context.Context) int { return a },
	}))
	_, err := e.Eval(context.Background(), "weird(1)", nil)
	// Arity check fires: the declared arity is 2, caller supplied 1.
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "expects 2 args")
}

// TestCtxInjection_NonCtxFuncUnchanged verifies a plain function with no
// ctx parameter still works exactly as before — the injection branch is
// a no-op for it.
func TestCtxInjection_NonCtxFuncUnchanged(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"double": func(x int) int { return x * 2 },
	}))
	v, err := e.Eval(context.Background(), "double(21)", nil)
	require.NoError(t, err)
	require.Equal(t, int(42), v)
}

// TestCtxInjection_CtxCancelPropagatesToUserFunc verifies that a user
// function receiving ctx can block on ctx.Done() and return promptly
// when the caller cancels.
func TestCtxInjection_CtxCancelPropagatesToUserFunc(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan struct{})
	// (T, error) return so goexpr propagates the error instead of
	// returning it as a value.
	e := New(WithFunctions(map[string]any{
		"wait": func(c context.Context) (any, error) {
			close(blocked)
			<-c.Done()
			return nil, c.Err()
		},
	}))

	done := make(chan error, 1)
	go func() {
		_, err := e.Eval(ctx, "wait()", nil)
		done <- err
	}()

	<-blocked
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.True(t, errors.Is(err, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("goexpr did not return after ctx cancellation propagated to user func")
	}
}

// TestScriptAdapter_UsesCtx ensures the script.Script adapter threads
// ctx into Program.Run so workflow-engine cancellation is observed.
func TestScriptAdapter_UsesCtx(t *testing.T) {
	e := New()
	cmp := e.Compiler()

	ctx, cancel := context.WithCancel(context.Background())
	s, err := cmp.Compile(ctx, "1 + 2")
	require.NoError(t, err)
	cancel()

	_, err = s.Evaluate(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}
