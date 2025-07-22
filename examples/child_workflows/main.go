package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

// Helper activity functions
func print(ctx context.Context, params map[string]any) (any, error) {
	message, ok := params["message"]
	if !ok {
		return nil, fmt.Errorf("print activity requires 'message' parameter")
	}
	fmt.Println(message)
	return message, nil
}

func processData(ctx context.Context, params map[string]any) (any, error) {
	data, ok := params["data"]
	if !ok {
		return nil, fmt.Errorf("process_data activity requires 'data' parameter")
	}

	// Simulate data processing
	result := fmt.Sprintf("Processed: %v", data)
	fmt.Printf("Processing data: %v -> %s\n", data, result)
	return result, nil
}

func validateData(ctx context.Context, params map[string]any) (any, error) {
	data, ok := params["data"]
	if !ok {
		return nil, fmt.Errorf("validate_data activity requires 'data' parameter")
	}

	// Simple validation - check if data is not empty
	valid := data != nil && fmt.Sprintf("%v", data) != ""
	result := map[string]any{
		"valid": valid,
		"data":  data,
	}

	fmt.Printf("Validating data: %v -> valid: %t\n", data, valid)
	return result, nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	fmt.Println("🚀 Child Workflow Example")
	fmt.Println("This example demonstrates:")
	fmt.Println("1. Parent workflow orchestrating child workflows")
	fmt.Println("2. Synchronous child workflow execution")
	fmt.Println("3. Data passing between parent and child workflows")
	fmt.Println("4. Child workflow results integration")
	fmt.Println()

	// Create workflow registry
	registry := workflow.NewMemoryWorkflowRegistry()

	// Define child workflow for data processing
	childWorkflow, err := workflow.New(workflow.Options{
		Name: "data-processor",
		Inputs: []*workflow.Input{
			{
				Name:        "raw_data",
				Type:        "string",
				Description: "Raw data to be processed",
				Required:    true,
			},
		},
		Steps: []*workflow.Step{
			{
				Name:     "Process Input Data",
				Activity: "process_data",
				Parameters: map[string]any{
					"data": "${inputs.raw_data}",
				},
				Store: "processed_result",
				Next:  []*workflow.Edge{{Step: "Return Result"}},
			},
			{
				Name:     "Return Result",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Child workflow completed: ${state.processed_result}",
				},
				End: true,
			},
		},
	})
	if err != nil {
		log.Fatal("Failed to create child workflow:", err)
	}

	// Register child workflow
	err = registry.Register(childWorkflow)
	if err != nil {
		log.Fatal("Failed to register child workflow:", err)
	}

	// Define validation child workflow
	validationWorkflow, err := workflow.New(workflow.Options{
		Name: "data-validator",
		Inputs: []*workflow.Input{
			{
				Name:        "data_to_validate",
				Type:        "any",
				Description: "Data to be validated",
				Required:    true,
			},
		},
		Steps: []*workflow.Step{
			{
				Name:     "Validate Input",
				Activity: "validate_data",
				Parameters: map[string]any{
					"data": "${inputs.data_to_validate}",
				},
				Store: "validation_result",
				Next:  []*workflow.Edge{{Step: "Report Validation"}},
			},
			{
				Name:     "Report Validation",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Validation result: ${state.validation_result.valid}",
				},
				End: true,
			},
		},
	})
	if err != nil {
		log.Fatal("Failed to create validation workflow:", err)
	}

	// Register validation workflow
	err = registry.Register(validationWorkflow)
	if err != nil {
		log.Fatal("Failed to register validation workflow:", err)
	}

	// Create activity list
	baseActivities := []workflow.Activity{
		workflow.NewActivityFunction("print", print),
		workflow.NewActivityFunction("process_data", processData),
		workflow.NewActivityFunction("validate_data", validateData),
		activities.NewScriptActivity(),
	}

	// Create child workflow executor
	childExecutor, err := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
		WorkflowRegistry: registry,
		Activities:       baseActivities,
		Logger:           logger,
		ActivityLogger:   workflow.NewNullActivityLogger(),
		Checkpointer:     workflow.NewNullCheckpointer(),
	})
	if err != nil {
		log.Fatal("Failed to create child workflow executor:", err)
	}

	// Add child workflow activity to the activity list
	allActivities := append(baseActivities, activities.NewChildWorkflowActivity(childExecutor))

	// Define main parent workflow
	parentWorkflow, err := workflow.New(workflow.Options{
		Name: "main-orchestrator",
		Steps: []*workflow.Step{
			{
				Name:     "Start Processing",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🎯 Starting main workflow orchestration...",
				},
				Next: []*workflow.Edge{{Step: "Set Initial Data"}},
			},
			{
				Name:     "Set Initial Data",
				Activity: "script",
				Parameters: map[string]any{
					"code": `state.raw_data = "Sample data to process"`,
				},
				Next: []*workflow.Edge{{Step: "Call Data Processor"}},
			},
			{
				Name:     "Call Data Processor",
				Activity: "workflow.child",
				Parameters: map[string]any{
					"workflow_name": "data-processor",
					"sync":          true,
					"timeout":       "30s",
					"inputs": map[string]any{
						"raw_data": "${state.raw_data}",
					},
				},
				Store: "processing_result",
				Next:  []*workflow.Edge{{Step: "Extract Result"}},
			},
			{
				Name:     "Extract Result",
				Activity: "script",
				Parameters: map[string]any{
					"code": `state.processed_data = state.processing_result.outputs.processed_result || "No result"`,
				},
				Next: []*workflow.Edge{{Step: "Call Data Validator"}},
			},
			{
				Name:     "Call Data Validator",
				Activity: "workflow.child",
				Parameters: map[string]any{
					"workflow_name": "data-validator",
					"sync":          true,
					"timeout":       "30s",
					"inputs": map[string]any{
						"data_to_validate": "${state.processed_data}",
					},
				},
				Store: "validation_result",
				Next:  []*workflow.Edge{{Step: "Check Validation"}},
			},
			{
				Name:     "Check Validation",
				Activity: "script",
				Parameters: map[string]any{
					"code": `state.is_valid = state.validation_result.outputs.validation_result.valid`,
				},
				Next: []*workflow.Edge{
					{Step: "Success", Condition: "state.is_valid == true"},
					{Step: "Failure", Condition: "state.is_valid == false"},
				},
			},
			{
				Name:     "Success",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ Processing completed successfully! Result: ${state.processed_data}",
				},
				End: true,
			},
			{
				Name:     "Failure",
				Activity: "print",
				Parameters: map[string]any{
					"message": "❌ Processing failed validation. Data: ${state.processed_data}",
				},
				End: true,
			},
		},
	})
	if err != nil {
		log.Fatal("Failed to create parent workflow:", err)
	}

	// Create and run parent workflow execution
	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       parentWorkflow,
		Inputs:         map[string]any{},
		Activities:     allActivities,
		Logger:         logger,
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   workflow.NewNullCheckpointer(),
	})
	if err != nil {
		log.Fatal("Failed to create execution:", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Println("Starting execution...")
	start := time.Now()

	if err := execution.Run(ctx); err != nil {
		log.Fatal("Execution failed:", err)
	}

	duration := time.Since(start)
	fmt.Printf("\n🎉 Execution completed in %v with status: %s\n", duration, execution.Status())

	if execution.Status() != workflow.ExecutionStatusCompleted {
		log.Fatal("Execution did not complete successfully")
	}

	fmt.Println("\n📊 Summary:")
	fmt.Println("- Main workflow orchestrated two child workflows")
	fmt.Println("- Data processing child workflow processed input data")
	fmt.Println("- Data validation child workflow validated the processed data")
	fmt.Println("- Results were passed between workflows via state management")
	fmt.Println("- All workflows executed synchronously as expected")
}
