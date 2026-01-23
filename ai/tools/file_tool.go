// Package tools provides built-in tools for AI agents.
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/deepnoodle-ai/workflow/ai"
)

// FileReadTool reads file contents.
type FileReadTool struct {
	allowedPaths []string // Empty means all paths allowed
}

// FileReadToolOptions configures FileReadTool.
type FileReadToolOptions struct {
	// AllowedPaths restricts which paths can be read.
	// Empty means all paths are allowed.
	AllowedPaths []string
}

// NewFileReadTool creates a new file read tool.
func NewFileReadTool(opts FileReadToolOptions) *FileReadTool {
	return &FileReadTool{
		allowedPaths: opts.AllowedPaths,
	}
}

func (t *FileReadTool) Name() string {
	return "read_file"
}

func (t *FileReadTool) Description() string {
	return "Read the contents of a file at the specified path"
}

func (t *FileReadTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("path", ai.StringProperty("The file path to read")).
		AddRequired("path")
}

func (t *FileReadTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "path is required and must be a string",
			Success: false,
		}, nil
	}

	// Check allowed paths if restricted
	if len(t.allowedPaths) > 0 {
		allowed := false
		absPath, err := filepath.Abs(path)
		if err != nil {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("invalid path: %v", err),
				Success: false,
			}, nil
		}
		for _, allowedPath := range t.allowedPaths {
			absAllowed, _ := filepath.Abs(allowedPath)
			if hasPrefix(absPath, absAllowed) {
				allowed = true
				break
			}
		}
		if !allowed {
			return &ai.ToolResult{
				Error:   "path not in allowed directories",
				Success: false,
			}, nil
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return &ai.ToolResult{
			Error:   fmt.Sprintf("failed to read file: %v", err),
			Success: false,
		}, nil
	}

	return &ai.ToolResult{
		Output:  string(content),
		Success: true,
	}, nil
}

// FileWriteTool writes content to a file.
type FileWriteTool struct {
	allowedPaths []string
}

// FileWriteToolOptions configures FileWriteTool.
type FileWriteToolOptions struct {
	AllowedPaths []string
}

// NewFileWriteTool creates a new file write tool.
func NewFileWriteTool(opts FileWriteToolOptions) *FileWriteTool {
	return &FileWriteTool{
		allowedPaths: opts.AllowedPaths,
	}
}

func (t *FileWriteTool) Name() string {
	return "write_file"
}

func (t *FileWriteTool) Description() string {
	return "Write content to a file at the specified path"
}

func (t *FileWriteTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("path", ai.StringProperty("The file path to write to")).
		AddProperty("content", ai.StringProperty("The content to write")).
		AddRequired("path", "content")
}

func (t *FileWriteTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "path is required and must be a string",
			Success: false,
		}, nil
	}

	content, ok := args["content"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "content is required and must be a string",
			Success: false,
		}, nil
	}

	// Check allowed paths if restricted
	if len(t.allowedPaths) > 0 {
		allowed := false
		absPath, err := filepath.Abs(path)
		if err != nil {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("invalid path: %v", err),
				Success: false,
			}, nil
		}
		for _, allowedPath := range t.allowedPaths {
			absAllowed, _ := filepath.Abs(allowedPath)
			if hasPrefix(absPath, absAllowed) {
				allowed = true
				break
			}
		}
		if !allowed {
			return &ai.ToolResult{
				Error:   "path not in allowed directories",
				Success: false,
			}, nil
		}
	}

	// Create parent directories if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &ai.ToolResult{
			Error:   fmt.Sprintf("failed to create directory: %v", err),
			Success: false,
		}, nil
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return &ai.ToolResult{
			Error:   fmt.Sprintf("failed to write file: %v", err),
			Success: false,
		}, nil
	}

	return &ai.ToolResult{
		Output:  fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path),
		Success: true,
	}, nil
}

// FileListTool lists files in a directory.
type FileListTool struct {
	allowedPaths []string
}

// FileListToolOptions configures FileListTool.
type FileListToolOptions struct {
	AllowedPaths []string
}

// NewFileListTool creates a new file list tool.
func NewFileListTool(opts FileListToolOptions) *FileListTool {
	return &FileListTool{
		allowedPaths: opts.AllowedPaths,
	}
}

func (t *FileListTool) Name() string {
	return "list_files"
}

func (t *FileListTool) Description() string {
	return "List files in a directory"
}

func (t *FileListTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("path", ai.StringProperty("The directory path to list")).
		AddProperty("recursive", ai.BooleanProperty("Whether to list recursively")).
		AddRequired("path")
}

func (t *FileListTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "path is required and must be a string",
			Success: false,
		}, nil
	}

	recursive, _ := args["recursive"].(bool)

	// Check allowed paths if restricted
	if len(t.allowedPaths) > 0 {
		allowed := false
		absPath, err := filepath.Abs(path)
		if err != nil {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("invalid path: %v", err),
				Success: false,
			}, nil
		}
		for _, allowedPath := range t.allowedPaths {
			absAllowed, _ := filepath.Abs(allowedPath)
			if hasPrefix(absPath, absAllowed) {
				allowed = true
				break
			}
		}
		if !allowed {
			return &ai.ToolResult{
				Error:   "path not in allowed directories",
				Success: false,
			}, nil
		}
	}

	var files []string
	if recursive {
		err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(path, p)
			if rel == "." {
				return nil
			}
			if info.IsDir() {
				files = append(files, rel+"/")
			} else {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("failed to walk directory: %v", err),
				Success: false,
			}, nil
		}
	} else {
		entries, err := os.ReadDir(path)
		if err != nil {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("failed to read directory: %v", err),
				Success: false,
			}, nil
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			files = append(files, name)
		}
	}

	result := ""
	for _, f := range files {
		result += f + "\n"
	}

	return &ai.ToolResult{
		Output:  result,
		Success: true,
	}, nil
}

// hasPrefix checks if path has the given prefix.
func hasPrefix(path, prefix string) bool {
	if len(path) < len(prefix) {
		return false
	}
	return path[:len(prefix)] == prefix
}

// Verify interface compliance.
var _ ai.Tool = (*FileReadTool)(nil)
var _ ai.Tool = (*FileWriteTool)(nil)
var _ ai.Tool = (*FileListTool)(nil)
