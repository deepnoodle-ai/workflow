package stores

import (
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/file"
)

// NewFileCheckpointer creates a new file-based checkpointer.
// If dataDir is empty, it defaults to ~/.deepnoodle/workflows/executions.
func NewFileCheckpointer(dataDir string) (workflow.Checkpointer, error) {
	return file.NewCheckpointer(dataDir)
}

// NewFileActivityLogger creates a new file-based activity logger.
func NewFileActivityLogger(directory string) workflow.ActivityLogger {
	return file.NewActivityLogger(directory)
}
