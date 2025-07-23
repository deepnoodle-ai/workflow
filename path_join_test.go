package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPathJoining(t *testing.T) {
	t.Run("basic path joining with variable extraction", func(t *testing.T) {
		// Create a workflow with path joining that extracts specific variables
		wf, err := New(Options{
			Name: "path-join-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "setup",
					Store:    "value",
					Next: []*Edge{
						{Step: "work_a", Path: "a"},
						{Step: "work_b", Path: "b"},
						{Step: "join", Path: "final"},
					},
				},
				{
					Name:     "work_a",
					Activity: "double",
					Store:    "result",
				},
				{
					Name:     "work_b",
					Activity: "triple",
					Store:    "result",
				},
				{
					Name: "join",
					Join: &JoinConfig{
						Paths: []string{"a", "b"},
						PathMappings: map[string]string{
							"a.result": "doubled",
							"b.result": "tripled",
						},
					},
					Next: []*Edge{{Step: "combine"}},
				},
				{
					Name:     "combine",
					Activity: "sum",
					Store:    "total",
				},
			},
			Outputs: []*Output{
				{Name: "total", Variable: "total", Path: "final"},
				{Name: "doubled", Variable: "doubled", Path: "final"},
				{Name: "tripled", Variable: "tripled", Path: "final"},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return 10, nil
				}),
				NewActivityFunction("double", func(ctx Context, params map[string]any) (any, error) {
					value, _ := ctx.GetVariable("value")
					return value.(int) * 2, nil
				}),
				NewActivityFunction("triple", func(ctx Context, params map[string]any) (any, error) {
					value, _ := ctx.GetVariable("value")
					return value.(int) * 3, nil
				}),
				NewActivityFunction("sum", func(ctx Context, params map[string]any) (any, error) {
					doubled, _ := ctx.GetVariable("doubled")
					tripled, _ := ctx.GetVariable("tripled")
					return doubled.(int) + tripled.(int), nil
				}),
			},
		})
		require.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify outputs
		outputs := execution.GetOutputs()
		require.Equal(t, 50, outputs["total"])   // 20 + 30
		require.Equal(t, 20, outputs["doubled"]) // 10 * 2
		require.Equal(t, 30, outputs["tripled"]) // 10 * 3
	})

	t.Run("path joining with full path state storage", func(t *testing.T) {
		// Test storing entire path state rather than extracting specific variables
		wf, err := New(Options{
			Name: "full-state-join-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "setup",
					Store:    "base",
					Next: []*Edge{
						{Step: "process_x", Path: "x"},
						{Step: "process_y", Path: "y"},
						{Step: "collect", Path: "final"},
					},
				},
				{
					Name:     "process_x",
					Activity: "work_x",
					Store:    "x_data",
				},
				{
					Name:     "process_y",
					Activity: "work_y",
					Store:    "y_data",
				},
				{
					Name: "collect",
					Join: &JoinConfig{
						Paths: []string{"x", "y"},
						PathMappings: map[string]string{
							"x": "path_x",
							"y": "path_y",
						},
					},
					Next: []*Edge{{Step: "analyze"}},
				},
				{
					Name:     "analyze",
					Activity: "combine",
					Store:    "result",
				},
			},
			Outputs: []*Output{
				{Name: "result", Variable: "result", Path: "final"},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return 5, nil
				}),
				NewActivityFunction("work_x", func(ctx Context, params map[string]any) (any, error) {
					ctx.SetVariable("x_meta", "x_processed")
					base, _ := ctx.GetVariable("base")
					return base.(int) * 4, nil
				}),
				NewActivityFunction("work_y", func(ctx Context, params map[string]any) (any, error) {
					ctx.SetVariable("y_meta", "y_processed")
					base, _ := ctx.GetVariable("base")
					return base.(int) * 6, nil
				}),
				NewActivityFunction("combine", func(ctx Context, params map[string]any) (any, error) {
					pathX, _ := ctx.GetVariable("path_x")
					pathY, _ := ctx.GetVariable("path_y")

					pathXMap := pathX.(map[string]any)
					pathYMap := pathY.(map[string]any)

					// Verify full path state was captured
					require.Equal(t, "x_processed", pathXMap["x_meta"])
					require.Equal(t, "y_processed", pathYMap["y_meta"])
					require.Equal(t, 20, pathXMap["x_data"]) // 5 * 4
					require.Equal(t, 30, pathYMap["y_data"]) // 5 * 6

					return pathXMap["x_data"].(int) + pathYMap["y_data"].(int), nil
				}),
			},
		})
		require.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify the result
		outputs := execution.GetOutputs()
		require.Equal(t, 50, outputs["result"]) // 20 + 30
	})
}
