package httpx

import (
	"context"

	"github.com/deepnoodle-ai/workflow"
)

func newTestContext() workflow.Context {
	return workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
		BranchLocalState: workflow.NewBranchLocalState(map[string]any{}, map[string]any{}),
	})
}
