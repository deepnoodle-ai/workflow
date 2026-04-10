// Compile once, evaluate many. Parsing an expression is cheap but not
// free; when the same predicate runs against many inputs, compile
// once via engine.Compile and reuse the *Program. Programs are
// immutable and safe to evaluate from multiple goroutines.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow/scripts/goexpr"
)

func main() {
	ctx := context.Background()

	engine := goexpr.New()

	// Compile the predicate once.
	pred, err := engine.Compile(`age >= 18 && contains(roles, "admin")`)
	if err != nil {
		panic(err)
	}

	people := []map[string]any{
		{"name": "Ada", "age": 36, "roles": []any{"admin", "editor"}},
		{"name": "Bob", "age": 17, "roles": []any{"admin"}},
		{"name": "Eve", "age": 41, "roles": []any{"viewer"}},
		{"name": "Sam", "age": 29, "roles": []any{"admin", "viewer"}},
	}

	fmt.Printf("predicate: %s\n\n", pred.Source())
	for _, p := range people {
		v, err := pred.Run(ctx, p)
		if err != nil {
			fmt.Printf("  %-5s  ERROR: %v\n", p["name"], err)
			continue
		}
		fmt.Printf("  %-5s => %v\n", p["name"], v)
	}

	// Quick micro-benchmark to illustrate why you'd want this.
	const N = 200_000
	start := time.Now()
	for i := 0; i < N; i++ {
		_, _ = pred.Run(ctx, people[0])
	}
	reused := time.Since(start)

	start = time.Now()
	for i := 0; i < N; i++ {
		_, _ = engine.Eval(ctx, pred.Source(), people[0])
	}
	recompiled := time.Since(start)

	fmt.Printf("\n%d evaluations:\n", N)
	fmt.Printf("  reused program:     %v\n", reused)
	fmt.Printf("  recompile each run: %v\n", recompiled)
}
