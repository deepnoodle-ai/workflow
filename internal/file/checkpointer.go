// Package file provides file-based implementations for workflow persistence.
package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// Checkpointer is a file-based implementation that persists checkpoints to disk.
type Checkpointer struct {
	dataDir string
}

// NewCheckpointer creates a new file-based checkpointer.
func NewCheckpointer(dataDir string) (*Checkpointer, error) {
	if dataDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		dataDir = filepath.Join(homeDir, ".deepnoodle", "workflows", "executions")
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory %s: %w", dataDir, err)
	}

	return &Checkpointer{dataDir: dataDir}, nil
}

// SaveCheckpoint saves the execution checkpoint to disk.
func (c *Checkpointer) SaveCheckpoint(ctx context.Context, checkpoint *domain.Checkpoint) error {
	executionDir := filepath.Join(c.dataDir, checkpoint.ExecutionID)
	if err := os.MkdirAll(executionDir, 0755); err != nil {
		return fmt.Errorf("failed to create execution directory: %w", err)
	}

	checkpointPath := filepath.Join(executionDir, fmt.Sprintf("checkpoint-%s.json", checkpoint.ID))
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	if err := os.WriteFile(checkpointPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write checkpoint file: %w", err)
	}

	latestPath := filepath.Join(executionDir, "latest.json")
	if err := c.updateLatestSymlink(checkpointPath, latestPath); err != nil {
		return fmt.Errorf("failed to update latest symlink: %w", err)
	}

	return nil
}

// LoadCheckpoint loads the latest checkpoint for an execution.
func (c *Checkpointer) LoadCheckpoint(ctx context.Context, executionID string) (*domain.Checkpoint, error) {
	latestPath := filepath.Join(c.dataDir, executionID, "latest.json")

	if _, err := os.Stat(latestPath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(latestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint file: %w", err)
	}

	var checkpoint domain.Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("failed to unmarshal checkpoint: %w", err)
	}

	return &checkpoint, nil
}

// DeleteCheckpoint removes all checkpoint data for an execution.
func (c *Checkpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	executionDir := filepath.Join(c.dataDir, executionID)
	if err := os.RemoveAll(executionDir); err != nil {
		return fmt.Errorf("failed to delete execution directory: %w", err)
	}
	return nil
}

// ListExecutions returns a list of all executions with their latest checkpoint info.
func (c *Checkpointer) ListExecutions(ctx context.Context) ([]*domain.ExecutionSummary, error) {
	entries, err := os.ReadDir(c.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*domain.ExecutionSummary{}, nil
		}
		return nil, fmt.Errorf("failed to read executions directory: %w", err)
	}

	var summaries []*domain.ExecutionSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		executionID := entry.Name()
		summary, err := c.getExecutionSummary(executionID)
		if err != nil {
			continue
		}
		if summary != nil {
			summaries = append(summaries, summary)
		}
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartTime.After(summaries[j].StartTime)
	})

	return summaries, nil
}

func (c *Checkpointer) getExecutionSummary(executionID string) (*domain.ExecutionSummary, error) {
	checkpoint, err := c.LoadCheckpoint(context.Background(), executionID)
	if err != nil || checkpoint == nil {
		return nil, err
	}

	return &domain.ExecutionSummary{
		ExecutionID:  checkpoint.ExecutionID,
		WorkflowName: checkpoint.WorkflowName,
		Status:       checkpoint.Status,
		StartTime:    checkpoint.StartTime,
		EndTime:      checkpoint.EndTime,
		Duration:     c.calculateDuration(checkpoint),
		Error:        checkpoint.Error,
	}, nil
}

func (c *Checkpointer) calculateDuration(checkpoint *domain.Checkpoint) time.Duration {
	if !checkpoint.EndTime.IsZero() {
		return checkpoint.EndTime.Sub(checkpoint.StartTime)
	}
	return checkpoint.CheckpointAt.Sub(checkpoint.StartTime)
}

func (c *Checkpointer) updateLatestSymlink(checkpointPath, latestPath string) error {
	if _, err := os.Lstat(latestPath); err == nil {
		if err := os.Remove(latestPath); err != nil {
			return fmt.Errorf("failed to remove existing latest symlink: %w", err)
		}
	}

	if strings.Contains(os.Getenv("OS"), "Windows") {
		data, err := os.ReadFile(checkpointPath)
		if err != nil {
			return fmt.Errorf("failed to read checkpoint for copy: %w", err)
		}
		return os.WriteFile(latestPath, data, 0644)
	}

	rel, err := filepath.Rel(filepath.Dir(latestPath), checkpointPath)
	if err != nil {
		return fmt.Errorf("failed to create relative path: %w", err)
	}

	return os.Symlink(rel, latestPath)
}

// Verify interface compliance.
var _ domain.Checkpointer = (*Checkpointer)(nil)
