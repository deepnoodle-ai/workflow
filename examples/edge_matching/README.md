# Edge Matching Strategies

This example demonstrates the difference between `EdgeMatchingAll` and `EdgeMatchingFirst` strategies for conditional edges.

## Key Concepts

### EdgeMatchingAll (Default)

When multiple edge conditions match, ALL matching edges are followed, creating parallel execution paths:

```go
{
    Name:                 "Decision Point",
    EdgeMatchingStrategy: workflow.EdgeMatchingAll,
    Next: []*workflow.Edge{
        {Step: "Handle Large", Condition: "50 > 30"},  // Matches - path created
        {Step: "Handle Medium", Condition: "50 < 70"}, // Matches - path created
        {Step: "Handle Small", Condition: "50 < 20"},  // No match
    },
}
```

With value 50, both "Handle Large" and "Handle Medium" execute in parallel.

### EdgeMatchingFirst

Only the FIRST matching edge is followed:

```go
{
    Name:                 "Decision Point",
    EdgeMatchingStrategy: workflow.EdgeMatchingFirst,
    Next: []*workflow.Edge{
        {Step: "Handle Large", Condition: "50 > 30"},  // First match - only this executes
        {Step: "Handle Medium", Condition: "50 < 70"}, // Would match but skipped
        {Step: "Handle Small", Condition: "50 < 20"},  // No match
    },
}
```

With value 50, only "Handle Large" executes because it's the first matching condition.

### When to Use Each Strategy

**EdgeMatchingAll** is useful for:
- Fan-out patterns where multiple paths should run in parallel
- Processing data through multiple independent pipelines
- Triggering multiple side effects based on overlapping conditions

**EdgeMatchingFirst** is useful for:
- Traditional switch/case logic where only one branch should execute
- Priority-based routing (put higher priority conditions first)
- Mutually exclusive conditions that could overlap

## Running the Example

```bash
go run main.go
```

Expected output shows both strategies:
```
=== Edge Matching Strategy Demo ===

Demo 1: EdgeMatchingAll Strategy
Evaluating conditions with ALL matching strategy...
Path A: Number is large (> 30)
Path B: Number is medium (< 70)

============================================================

Demo 2: EdgeMatchingFirst Strategy
Evaluating conditions with FIRST matching strategy...
Only Path: Number is large (> 30) - first match wins!
```
