// Compile once, evaluate many. Parsing an expression is cheap but not
// free; when the same predicate runs against many inputs, compile
// once via expr.Compile and reuse the *Program. Programs are
// immutable and safe to evaluate from multiple goroutines.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	source := `age >= 18 && contains(roles, "admin")`

	// Compile the predicate once.
	pred, err := expr.Compile(source, expr.WithBuiltins())
	if err != nil {
		fmt.Fprintf(os.Stderr, "compile failed: %s\n", err)
		os.Exit(1)
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
		if _, err := pred.Run(ctx, people[0]); err != nil {
			fmt.Fprintf(os.Stderr, "reused run failed: %s\n", err)
			os.Exit(1)
		}
	}
	reused := time.Since(start)

	start = time.Now()
	for i := 0; i < N; i++ {
		p, err := expr.Compile(source, expr.WithBuiltins())
		if err != nil {
			fmt.Fprintf(os.Stderr, "recompile failed: %s\n", err)
			os.Exit(1)
		}
		if _, err := p.Run(ctx, people[0]); err != nil {
			fmt.Fprintf(os.Stderr, "recompiled run failed: %s\n", err)
			os.Exit(1)
		}
	}
	recompiled := time.Since(start)

	fmt.Printf("\n%d evaluations:\n", N)
	fmt.Printf("  reused program:     %v\n", reused)
	fmt.Printf("  recompile each run: %v\n", recompiled)
}
