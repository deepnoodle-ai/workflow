package stores

import (
	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/file"
)

// NewFileCheckpointer creates a new file-based checkpointer.
// If dataDir is empty, it defaults to ~/.deepnoodle/workflows/executions.
func NewFileCheckpointer(dataDir string) (domain.Checkpointer, error) {
	return file.NewCheckpointer(dataDir)
}

// NewFileActivityLogger creates a new file-based activity logger.
func NewFileActivityLogger(directory string) domain.ActivityLogger {
	return file.NewActivityLogger(directory)
}
