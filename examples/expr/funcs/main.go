// Custom functions: WithFunctions registers any Go function as a
// callable identifier. Arguments are converted to the declared
// parameter types automatically, and (T, error) return pairs are
// surfaced the same way as any other error.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/deepnoodle-ai/expr"
)

// titleCase is a tiny ASCII title-caser. The example avoids depending
// on golang.org/x/text so the workflow module keeps a single direct
// dependency (github.com/deepnoodle-ai/expr).
func titleCase(s string) string {
	runes := []rune(s)
	atStart := true
	for i, r := range runes {
		switch {
		case unicode.IsSpace(r):
			atStart = true
		case atStart:
			runes[i] = unicode.ToUpper(r)
			atStart = false
		default:
			runes[i] = unicode.ToLower(r)
		}
	}
	return string(runes)
}

func main() {
	ctx := context.Background()

	opts := []expr.Option{
		expr.WithFunctions(map[string]any{
			// plain functions
			"upper":     strings.ToUpper,
			"title":     titleCase,
			"hasPrefix": strings.HasPrefix,

			// variadic
			"join": func(sep string, parts ...string) string {
				return strings.Join(parts, sep)
			},

			// fallible — (T, error) signatures propagate errors naturally
			"parseDate": func(s string) (time.Time, error) {
				return time.Parse("2006-01-02", s)
			},

			// context-aware: ctx is injected automatically when the first
			// parameter is context.Context
			"deadline": func(ctx context.Context) string {
				if d, ok := ctx.Deadline(); ok {
					return d.Format(time.RFC3339)
				}
				return "no deadline"
			},
		}),
	}

	env := map[string]any{
		"name": "ada lovelace",
		"slug": "workflow-expr-demo",
		"date": "2026-04-10",
		"bad":  "not-a-date",
	}

	exprs := []string{
		`upper(name)`,
		`title(name)`,
		`hasPrefix(slug, "workflow")`,
		`join("-", "a", "b", "c")`,
		`parseDate(date)`,
		`parseDate(bad)`, // surfaces the underlying error
		`deadline()`,     // reads the ctx passed to Run
	}

	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(5*time.Second))
	defer cancel()

	fmt.Println("expressions using custom functions:")
	for _, code := range exprs {
		p, err := expr.Compile(code, opts...)
		if err != nil {
			fmt.Printf("  %-36s  ERROR: %v\n", code, err)
			continue
		}
		v, err := p.Run(ctx, env)
		if err != nil {
			fmt.Printf("  %-36s  ERROR: %v\n", code, err)
			continue
		}
		fmt.Printf("  %-36s => %v\n", code, v)
	}
}
