package activities

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/deepnoodle-ai/workflow"
)

// FileInput defines the input parameters for the file activity
type FileInput struct {
	Operation   string `json:"operation"`   // read, write, append, delete, exists, mkdir, list
	Path        string `json:"path"`        // file or directory path
	Content     string `json:"content"`     // content to write (for write/append operations)
	Permissions string `json:"permissions"` // file permissions (e.g., "0644", "0755")
	CreateDirs  bool   `json:"create_dirs"` // create parent directories if they don't exist
}

// FileActivity can be used to perform file operations
type FileActivity struct{}

func NewFileActivity() workflow.Activity {
	return workflow.NewTypedActivity(&FileActivity{})
}

func (a *FileActivity) Name() string {
	return "file"
}

func (a *FileActivity) Execute(ctx workflow.Context, params FileInput) (any, error) {
	if params.Path == "" {
		return nil, fmt.Errorf("path cannot be empty")
	}
	if params.Operation == "" {
		params.Operation = "read"
	}

	switch strings.ToLower(params.Operation) {
	case "read":
		content, err := os.ReadFile(params.Path)
		if err != nil {
			return nil, err
		}
		return string(content), nil

	case "write":
		// Create parent directories if requested
		if params.CreateDirs {
			if err := os.MkdirAll(filepath.Dir(params.Path), 0755); err != nil {
				return nil, err
			}
		}
		// Parse permissions
		perm := fs.FileMode(0644)
		if params.Permissions != "" {
			if parsed, err := parsePermissions(params.Permissions); err == nil {
				perm = parsed
			}
		}
		if err := os.WriteFile(params.Path, []byte(params.Content), perm); err != nil {
			return nil, err
		}
		return true, nil

	case "append":
		file, err := os.OpenFile(params.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		if _, err := file.WriteString(params.Content); err != nil {
			return nil, err
		}
		return true, nil

	case "delete":
		if err := os.Remove(params.Path); err != nil {
			return nil, err
		}
		return true, nil

	case "exists":
		if _, err := os.Stat(params.Path); err == nil {
			return true, nil
		} else {
			return false, nil
		}

	case "mkdir":
		perm := fs.FileMode(0755)
		if params.Permissions != "" {
			if parsed, err := parsePermissions(params.Permissions); err == nil {
				perm = parsed
			}
		}
		var err error
		if params.CreateDirs {
			err = os.MkdirAll(params.Path, perm)
		} else {
			err = os.Mkdir(params.Path, perm)
		}
		if err != nil {
			return nil, err
		}
		return true, nil

	case "list":
		entries, err := os.ReadDir(params.Path)
		if err != nil {
			return nil, err
		}
		files := make([]string, len(entries))
		for i, entry := range entries {
			if entry.IsDir() {
				files[i] = entry.Name() + "/"
			} else {
				files[i] = entry.Name()
			}
		}
		return files, nil

	default:
		return nil, fmt.Errorf("unsupported operation: %s", params.Operation)
	}
}

// parsePermissions converts a string permission to fs.FileMode
func parsePermissions(perm string) (fs.FileMode, error) {
	// Handle octal permissions like "0644", "0755"
	if strings.HasPrefix(perm, "0") {
		var mode uint32
		if _, err := fmt.Sscanf(perm, "%o", &mode); err != nil {
			return 0, err
		}
		return fs.FileMode(mode), nil
	}

	// Handle decimal permissions
	var mode uint32
	if _, err := fmt.Sscanf(perm, "%d", &mode); err != nil {
		return 0, err
	}
	return fs.FileMode(mode), nil
}
