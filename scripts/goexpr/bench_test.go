package goexpr

import "testing"

// benchEnv is the environment used by the compile/run benchmarks. It
// mirrors the kind of state a workflow condition typically touches:
// nested maps, a slice, and a few scalar values.
func benchEnv() map[string]any {
	return map[string]any{
		"state": map[string]any{
			"counter": int64(10),
			"limit":   int64(100),
			"name":    "Alice",
			"items":   []any{int64(1), int64(2), int64(3), int64(4), int64(5)},
			"user": map[string]any{
				"age":    int64(30),
				"active": true,
			},
		},
		"inputs": map[string]any{
			"multiplier": int64(3),
			"prefix":     "workflow:",
		},
	}
}

// benchExprs covers the shapes goexpr is most often asked to run: plain
// comparisons, field chains, builtin calls, string concatenation, and
// short-circuit logic. If any of these regress materially, it will show
// up first.
var benchExprs = map[string]string{
	"literal":     "42",
	"arith":       "1 + 2 * 3",
	"condition":   "state.counter < state.limit && state.user.active",
	"nested_sel":  "state.user.age >= 18",
	"index":       "state.items[2]",
	"builtin_len": "len(state.items) > 3",
	"template":    `inputs.prefix + string(state.counter)`,
	"has_key":     `has(state.user, "active")`,
	"contains":    `contains(state.items, 3)`,
	"mixed":       `state.counter * inputs.multiplier + len(state.items)`,
}

func BenchmarkCompile(b *testing.B) {
	e := New()
	for name, src := range benchExprs {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, err := e.Compile(src)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRun(b *testing.B) {
	e := New()
	env := benchEnv()
	for name, src := range benchExprs {
		prog, err := e.Compile(src)
		if err != nil {
			b.Fatalf("%s: compile failed: %v", name, err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := prog.Run(ctx, env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCompileRun measures the combined cost — relevant for
// consumers that evaluate an expression once and throw it away.
func BenchmarkCompileRun(b *testing.B) {
	e := New()
	env := benchEnv()
	src := "state.counter * inputs.multiplier + len(state.items)"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		prog, err := e.Compile(src)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := prog.Run(ctx, env); err != nil {
			b.Fatal(err)
		}
	}
}
