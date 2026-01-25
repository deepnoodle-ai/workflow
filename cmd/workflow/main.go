package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/stores"
	"github.com/deepnoodle-ai/wonton/color"
)

// CLI configuration
type Config struct {
	WorkflowFile  string
	Inputs        map[string]interface{}
	LogsDir       string
	ExecutionsDir string
	Timeout       time.Duration
	Verbose       bool
	JSON          bool
	ShowInputs    bool
	ShowOutputs   bool
	EnableChild   bool
}

func main() {
	config := parseFlags()

	// Validate required arguments
	if config.WorkflowFile == "" {
		fmt.Println(color.Red.Sprintf("Error: workflow file is required"))
		flag.Usage()
		os.Exit(1)
	}

	// Check if workflow file exists
	if _, err := os.Stat(config.WorkflowFile); os.IsNotExist(err) {
		fmt.Println(color.Red.Sprintf("Error: workflow file '%s' not found", config.WorkflowFile))
		os.Exit(1)
	}

	// Set up logging
	logger := setupLogger(config.Verbose)

	// Load workflow from YAML file
	fmt.Println(color.Blue.Sprintf("Loading workflow from: %s", config.WorkflowFile))
	wf, err := workflow.LoadFile(config.WorkflowFile)
	if err != nil {
		log.Fatalf("Failed to load workflow: %v", err)
	}

	fmt.Println(color.Cyan.Sprintf("Workflow: %s", wf.Name()))
	if wf.Description() != "" {
		fmt.Println(color.White.Sprintf("Description: %s", wf.Description()))
	}

	// Show inputs if requested and exit
	if config.ShowInputs {
		showWorkflowInputs(wf)
		return
	}

	// Validate and prepare inputs
	inputs, err := prepareInputs(wf, config.Inputs)
	if err != nil {
		log.Fatalf("Input validation failed: %v", err)
	}

	// Create activity registry with all available activities
	activityRegistry := createActivityRegistry(config, logger)

	// Set up activity logger
	var activityLogger workflow.ActivityLogger
	if config.LogsDir != "" {
		activityLogger = stores.NewFileActivityLogger(config.LogsDir)
		fmt.Println(color.Blue.Sprintf("Activity logs: %s", config.LogsDir))
	} else {
		activityLogger = workflow.NewNullActivityLogger()
	}

	// Set up checkpointer
	var checkpointer workflow.Checkpointer
	if config.ExecutionsDir != "" {
		checkpointer, err = stores.NewFileCheckpointer(config.ExecutionsDir)
		if err != nil {
			log.Fatalf("Failed to create checkpointer: %v", err)
		}
		fmt.Println(color.Blue.Sprintf("Checkpoints: %s", config.ExecutionsDir))
	} else {
		checkpointer = workflow.NewNullCheckpointer()
	}

	// Create execution
	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		Inputs:         inputs,
		Activities:     activityRegistry,
		Logger:         logger,
		ActivityLogger: activityLogger,
		Checkpointer:   checkpointer,
	})
	if err != nil {
		log.Fatalf("Failed to create execution: %v", err)
	}

	// Execute workflow with timeout
	ctx := context.Background()
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
		fmt.Println(color.Yellow.Sprintf("Timeout: %v", config.Timeout))
	}

	fmt.Println(color.Green.Sprintf("Starting execution (ID: %s)...\n", execution.ID()))

	startTime := time.Now()
	err = execution.Run(ctx)
	duration := time.Since(startTime)

	// Show execution results
	showExecutionResults(execution, err, duration, config)
}

func parseFlags() *Config {
	config := &Config{
		Inputs: make(map[string]interface{}),
	}

	// Define flags
	flag.StringVar(&config.WorkflowFile, "file", "", "Path to the YAML workflow definition file (required)")
	flag.StringVar(&config.WorkflowFile, "f", "", "Path to the YAML workflow definition file (shorthand)")

	var inputFlags stringSlice
	flag.Var(&inputFlags, "input", "Input parameter in format key=value (can be used multiple times)")
	flag.Var(&inputFlags, "i", "Input parameter in format key=value (shorthand, can be used multiple times)")

	flag.StringVar(&config.LogsDir, "logs", "", "Directory to store activity logs (optional)")
	flag.StringVar(&config.LogsDir, "l", "", "Directory to store activity logs (shorthand)")

	flag.StringVar(&config.ExecutionsDir, "executions", "", "Directory to store execution checkpoints (optional)")
	flag.StringVar(&config.ExecutionsDir, "e", "", "Directory to store execution checkpoints (shorthand)")

	flag.DurationVar(&config.Timeout, "timeout", 0, "Execution timeout (e.g., 30s, 5m, 1h)")
	flag.DurationVar(&config.Timeout, "t", 0, "Execution timeout (shorthand)")

	flag.BoolVar(&config.Verbose, "verbose", false, "Enable verbose logging")
	flag.BoolVar(&config.Verbose, "v", false, "Enable verbose logging (shorthand)")

	flag.BoolVar(&config.JSON, "json", false, "Output results in JSON format")
	flag.BoolVar(&config.ShowInputs, "show-inputs", false, "Show workflow input requirements and exit")
	flag.BoolVar(&config.ShowOutputs, "show-outputs", true, "Show workflow outputs after execution (default: true)")
	flag.BoolVar(&config.EnableChild, "enable-child-workflows", false, "Enable child workflow support")

	// Custom usage
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Workflow CLI - Execute YAML-defined workflows

Usage: %s [options] -file <workflow.yaml>

Examples:
  # Execute a simple workflow
  %s -file example.yaml

  # Execute with inputs and logging
  %s -file workflow.yaml -input name=John -input count=5 -logs ./logs

  # Execute with timeout and checkpointing
  %s -file workflow.yaml -timeout 30s -executions ./checkpoints

Options:
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()

		fmt.Fprintf(os.Stderr, `
Supported Activities:
  print          - Print messages to console
  script         - Execute JavaScript-like code using Risor
  time           - Get current timestamp
  wait           - Wait for a specified duration
  fail           - Intentionally fail with a message
  http           - Make HTTP requests
  file           - Read, write, and manage files
  json           - Parse, query, and stringify JSON
  random         - Generate random numbers, strings, and UUIDs
  shell          - Execute shell commands
  workflow.child - Execute child workflows (with -enable-child-workflows)

Input Format:
  Use -input key=value for each input parameter.
  Values are parsed as JSON if possible, otherwise as strings.

`)
	}

	flag.Parse()

	// Parse input flags
	for _, input := range inputFlags {
		parts := strings.SplitN(input, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid input format '%s'. Use key=value\n", input)
			os.Exit(1)
		}

		key, value := parts[0], parts[1]

		// Try to parse as JSON, fallback to string
		var parsedValue interface{}
		if err := json.Unmarshal([]byte(value), &parsedValue); err != nil {
			parsedValue = value // Use as string if JSON parsing fails
		}

		config.Inputs[key] = parsedValue
	}

	return config
}

// Custom flag type for handling multiple input values
type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func setupLogger(verbose bool) *slog.Logger {
	level := slog.LevelError
	if verbose {
		level = slog.LevelInfo
	}

	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

func createActivityRegistry(config *Config, logger *slog.Logger) []workflow.Activity {
	activityList := []workflow.Activity{
		activities.NewPrintActivity(),
		activities.NewTimeActivity(),
		activities.NewWaitActivity(),
		activities.NewFailActivity(),
		activities.NewHTTPActivity(),
		activities.NewFileActivity(),
		activities.NewJSONActivity(),
		activities.NewRandomActivity(),
		activities.NewShellActivity(),
	}

	// Add child workflow support if enabled
	if config.EnableChild {
		registry := workflow.NewMemoryWorkflowRegistry()
		childExecutor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
			WorkflowRegistry: registry,
			Activities:       activityList, // Base activities for child workflows
			Logger:           logger,
			ActivityLogger:   workflow.NewNullActivityLogger(),
			Checkpointer:     workflow.NewNullCheckpointer(),
		})
		if err != nil {
			log.Fatalf("Failed to create child workflow executor: %v", err)
		}

		activityList = append(activityList, activities.NewChildWorkflowActivity(childExecutor))
		fmt.Println(color.Magenta.Sprintf("Child workflow support enabled"))
	}

	return activityList
}

func showWorkflowInputs(wf *workflow.Workflow) {
	inputs := wf.Inputs()
	if len(inputs) == 0 {
		fmt.Println(color.Blue.Sprintf("No inputs required"))
		return
	}

	fmt.Println(color.Blue.Sprintf("Workflow inputs:"))
	for _, input := range inputs {
		required := ""
		defaultValue := ""
		if input.Default != nil {
			if defaultBytes, err := json.Marshal(input.Default); err == nil {
				defaultValue = fmt.Sprintf(" [default: %s]", string(defaultBytes))
			}
		} else {
			required = " (required)"
		}

		fmt.Printf("  %s (%s)%s%s\n", input.Name, input.Type, required, defaultValue)
		if input.Description != "" {
			fmt.Printf("    %s\n", input.Description)
		}
	}
}

func prepareInputs(wf *workflow.Workflow, providedInputs map[string]interface{}) (map[string]interface{}, error) {
	inputs := make(map[string]interface{})

	// Validate required inputs and apply defaults
	for _, input := range wf.Inputs() {
		if value, provided := providedInputs[input.Name]; provided {
			inputs[input.Name] = value
		} else if input.Default != nil {
			inputs[input.Name] = input.Default
		} else if input.IsRequired() {
			return nil, fmt.Errorf("required input '%s' not provided", input.Name)
		}
	}

	// Check for unknown inputs
	for name := range providedInputs {
		found := false
		for _, input := range wf.Inputs() {
			if input.Name == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown input '%s'", name)
		}
	}

	return inputs, nil
}

func showExecutionResults(execution *workflow.Execution, err error, duration time.Duration, config *Config) {
	status := execution.Status()

	// Show execution summary
	fmt.Println(color.White.Sprintf("Execution completed in %v", duration))
	fmt.Println(color.White.Sprintf("Status: %s", status))

	if err != nil {
		fmt.Println(color.Red.Sprintf("Error: %v", err))
		if status != domain.ExecutionStatusCompleted {
			os.Exit(1)
		}
	} else {
		fmt.Println(color.Green.Sprintf("Execution successful!"))
	}

	// Show outputs
	if config.ShowOutputs {
		outputs := execution.GetOutputs()
		if len(outputs) > 0 {
			fmt.Printf("\n")
			fmt.Println(color.Magenta.Sprintf("Outputs:"))
			if config.JSON {
				outputBytes, err := json.MarshalIndent(outputs, "", "  ")
				if err != nil {
					fmt.Printf("Error formatting outputs: %v\n", err)
				} else {
					fmt.Println(string(outputBytes))
				}
			} else {
				for key, value := range outputs {
					if valueBytes, err := json.Marshal(value); err == nil {
						fmt.Printf("  %s: %s\n", key, string(valueBytes))
					} else {
						fmt.Printf("  %s: %v\n", key, value)
					}
				}
			}
		}
	}
}
