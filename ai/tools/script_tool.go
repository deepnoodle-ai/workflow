package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow/ai"
)

// ShellTool executes shell commands.
type ShellTool struct {
	shell           string
	timeout         time.Duration
	allowedCommands []string // Empty means all commands allowed
	workingDir      string
}

// ShellToolOptions configures ShellTool.
type ShellToolOptions struct {
	// Shell to use (default: "sh").
	Shell string

	// Timeout for commands (default: 60 seconds).
	Timeout time.Duration

	// AllowedCommands restricts which commands can be run.
	// Empty means all commands are allowed.
	AllowedCommands []string

	// WorkingDir is the working directory for commands.
	WorkingDir string
}

// NewShellTool creates a new shell tool.
func NewShellTool(opts ShellToolOptions) *ShellTool {
	shell := opts.Shell
	if shell == "" {
		shell = "sh"
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	return &ShellTool{
		shell:           shell,
		timeout:         timeout,
		allowedCommands: opts.AllowedCommands,
		workingDir:      opts.WorkingDir,
	}
}

func (t *ShellTool) Name() string {
	return "run_command"
}

func (t *ShellTool) Description() string {
	return "Execute a shell command and return the output"
}

func (t *ShellTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("command", ai.StringProperty("The shell command to execute")).
		AddProperty("working_dir", ai.StringProperty("Working directory for the command")).
		AddRequired("command")
}

func (t *ShellTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	command, ok := args["command"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "command is required and must be a string",
			Success: false,
		}, nil
	}

	// Check allowed commands
	if len(t.allowedCommands) > 0 {
		allowed := false
		cmdParts := strings.Fields(command)
		if len(cmdParts) > 0 {
			for _, allowedCmd := range t.allowedCommands {
				if cmdParts[0] == allowedCmd {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return &ai.ToolResult{
				Error:   "command not in allowed commands list",
				Success: false,
			}, nil
		}
	}

	// Set up context with timeout
	execCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// Create command
	cmd := exec.CommandContext(execCtx, t.shell, "-c", command)

	// Set working directory
	workDir := t.workingDir
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		workDir = wd
	}
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()

	// Build result
	result := map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result["exit_code"] = exitErr.ExitCode()
		} else {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("failed to run command: %v", err),
				Success: false,
			}, nil
		}
	}

	resultJSON, _ := json.Marshal(result)

	exitCode, _ := result["exit_code"].(int)
	return &ai.ToolResult{
		Output:  string(resultJSON),
		Success: exitCode == 0,
	}, nil
}

// PythonTool executes Python code.
type PythonTool struct {
	pythonPath string
	timeout    time.Duration
	workingDir string
}

// PythonToolOptions configures PythonTool.
type PythonToolOptions struct {
	// PythonPath is the path to the Python interpreter (default: "python3").
	PythonPath string

	// Timeout for execution (default: 60 seconds).
	Timeout time.Duration

	// WorkingDir is the working directory.
	WorkingDir string
}

// NewPythonTool creates a new Python tool.
func NewPythonTool(opts PythonToolOptions) *PythonTool {
	pythonPath := opts.PythonPath
	if pythonPath == "" {
		pythonPath = "python3"
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	return &PythonTool{
		pythonPath: pythonPath,
		timeout:    timeout,
		workingDir: opts.WorkingDir,
	}
}

func (t *PythonTool) Name() string {
	return "run_python"
}

func (t *PythonTool) Description() string {
	return "Execute Python code and return the output"
}

func (t *PythonTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("code", ai.StringProperty("Python code to execute")).
		AddRequired("code")
}

func (t *PythonTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	code, ok := args["code"].(string)
	if !ok {
		return &ai.ToolResult{
			Error:   "code is required and must be a string",
			Success: false,
		}, nil
	}

	// Set up context with timeout
	execCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// Create command
	cmd := exec.CommandContext(execCtx, t.pythonPath, "-c", code)

	if t.workingDir != "" {
		cmd.Dir = t.workingDir
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()

	// Build result
	result := map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result["exit_code"] = exitErr.ExitCode()
		} else {
			return &ai.ToolResult{
				Error:   fmt.Sprintf("failed to run Python: %v", err),
				Success: false,
			}, nil
		}
	}

	resultJSON, _ := json.Marshal(result)

	exitCode, _ := result["exit_code"].(int)
	return &ai.ToolResult{
		Output:  string(resultJSON),
		Success: exitCode == 0,
	}, nil
}

// Verify interface compliance.
var _ ai.Tool = (*ShellTool)(nil)
var _ ai.Tool = (*PythonTool)(nil)
