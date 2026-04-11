// Higher-order builtins: map, filter, any, all, find, count. Each
// takes a collection and a predicate expression that is re-evaluated
// per element with `it` (the current element) and `index` (the 0-based
// position) in scope.
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	env := map[string]any{
		"users": []any{
			map[string]any{"name": "Ada", "age": 36, "active": true},
			map[string]any{"name": "Alan", "age": 41, "active": false},
			map[string]any{"name": "Grace", "age": 29, "active": true},
			map[string]any{"name": "Linus", "age": 54, "active": true},
		},
		"nums": []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}

	exprs := []string{
		// map: transform every element
		`map(users, it.name)`,
		`map(nums, it * it)`,

		// filter: keep elements matching the predicate
		`filter(users, it.active)`,
		`filter(nums, it % 2 == 0)`,

		// any / all: predicate existence checks
		`any(users, it.age > 50)`,
		`all(users, it.age >= 18)`,

		// find: first match (nil if none)
		`find(users, it.name == "Grace")`,
		`find(nums, it > 100)`,

		// count: how many matched
		`count(users, it.active)`,
		`count(nums, it > 5)`,

		// `index` is bound alongside `it`
		`map(users, sprintf("%d. %s", index, it.name))`,

		// nested forms: the inner `it` shadows the outer
		`map(users, map(nums, it))[0]`,
	}

	fmt.Println("higher-order expressions:")
	for _, code := range exprs {
		p, err := expr.Compile(code, expr.WithBuiltins())
		if err != nil {
			fmt.Printf("  %-52s  ERROR: %v\n", code, err)
			continue
		}
		v, err := p.Run(ctx, env)
		if err != nil {
			fmt.Printf("  %-52s  ERROR: %v\n", code, err)
			continue
		}
		fmt.Printf("  %-52s => %v\n", code, v)
	}
}
