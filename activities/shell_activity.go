package activities

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// ShellInput defines the input parameters for the shell activity
type ShellInput struct {
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	WorkingDir  string            `json:"working_dir"`
	Environment map[string]string `json:"environment"`
	Timeout     float64           `json:"timeout"` // in seconds, 0 means no timeout
}

// ShellActivity can be used to execute shell commands
type ShellActivity struct{}

func NewShellActivity() workflow.Activity {
	return workflow.NewTypedActivity(&ShellActivity{})
}

func (a *ShellActivity) Name() string {
	return "shell"
}

func (a *ShellActivity) Execute(ctx context.Context, params ShellInput) (map[string]any, error) {
	if params.Command == "" {
		return nil, fmt.Errorf("command cannot be empty")
	}

	// Create command with context for timeout support
	var cmd *exec.Cmd
	if params.Timeout > 0 {
		timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(params.Timeout*float64(time.Second)))
		defer cancel()
		cmd = exec.CommandContext(timeoutCtx, params.Command, params.Args...)
	} else {
		cmd = exec.CommandContext(ctx, params.Command, params.Args...)
	}

	// Set working directory if specified
	if params.WorkingDir != "" {
		cmd.Dir = params.WorkingDir
	}

	// Set environment variables
	if len(params.Environment) > 0 {
		cmd.Env = os.Environ()
		for key, value := range params.Environment {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Execute command and capture output
	stdout, err := cmd.Output()
	var stderr []byte
	var exitCode int

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			stderr = exitError.Stderr
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
		} else {
			return nil, fmt.Errorf("failed to execute command: %w", err)
		}
	}

	return map[string]any{
		"stdout":    strings.TrimSpace(string(stdout)),
		"stderr":    strings.TrimSpace(string(stderr)),
		"exit_code": exitCode,
		"success":   exitCode == 0,
	}, nil
}
