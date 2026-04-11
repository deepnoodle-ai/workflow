// Basic expr usage: literals, arithmetic, comparisons, logical ops, and
// identifier lookups against a map[string]any environment.
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	env := map[string]any{
		"user": map[string]any{
			"name":  "Ada",
			"age":   36,
			"roles": []any{"admin", "editor"},
		},
		"threshold": 18,
	}

	exprs := []string{
		// arithmetic
		`1 + 2 * 3`,
		`(10 - 4) / 2`,
		`7 % 4`,

		// string concat
		`"hello, " + user.name`,

		// selector access (user.name, user.age)
		`user.name`,
		`user.age`,

		// comparisons
		`user.age >= threshold`,
		`user.name == "Ada"`,

		// logical operators with short-circuit
		`user.age > 18 && user.name != ""`,
		`user.age < 18 || user.age >= 65`,

		// index into a slice
		`user.roles[0]`,

		// unary
		`!(user.age < 18)`,
		`-user.age`,

		// nil safety via the `contains` builtin on a slice
		`contains(user.roles, "admin")`,

		// numeric coercion: int + float => float
		`user.age + 0.5`,
	}

	fmt.Println("basic expr expressions:")
	for _, code := range exprs {
		show(ctx, code, env)
	}
}

func show(ctx context.Context, code string, env any) {
	p, err := expr.Compile(code, expr.WithBuiltins())
	if err != nil {
		fmt.Printf("  %-42s  ERROR: %v\n", code, err)
		return
	}
	v, err := p.Run(ctx, env)
	if err != nil {
		fmt.Printf("  %-42s  ERROR: %v\n", code, err)
		return
	}
	fmt.Printf("  %-42s => %v\n", code, v)
}
