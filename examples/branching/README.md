# Branching Example

This example demonstrates conditional logic and decision trees in workflows.

## What it shows

- **Conditional Edges**: Multiple conditions on workflow transitions  
- **Complex Decision Trees**: Routing based on multiple state variables
- **State-based Logic**: Different paths based on computed results
- **Script Activities**: In-workflow calculations and data manipulation

## The Workflow

1. **Start** - Welcome message
2. **Generate Random Number** - Creates a random number (1-100)
3. **Display Number** - Shows the generated number
4. **Check Prime** - Determines if the number is prime
5. **Categorize Number** - Classifies as small/medium/large
6. **Branching Logic** - Routes to one of 6 different paths:
   - Prime Small (1-9)
   - Prime Medium (10-49) 
   - Prime Large (50+)
   - Composite Small (1-9)
   - Composite Medium (10-49)
   - Composite Large (50+)
7. **Conditional Processing**:
   - Prime numbers: Special messages about their properties
   - Composite numbers: Calculate and display all factors
8. **Final Summary** - Analysis results and conclusion

## Running the Example

```bash
cd examples/branching  
go run main.go
```

## Key Concepts Demonstrated

- **Multiple Conditions**: `state.is_prime && state.category == 'small'`
- **Boolean Logic**: Combining multiple state variables in conditions
- **Dynamic Routing**: Different execution paths based on runtime data
- **Script Integration**: JavaScript-like code for complex calculations
- **State Interpolation**: Using `${state.variable}` in parameters and messages 
