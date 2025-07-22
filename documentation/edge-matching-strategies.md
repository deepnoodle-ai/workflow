# Edge Matching Strategies

This document explains the edge matching strategies feature that controls how workflow steps handle multiple matching edges.

## Overview

Previously, workflows would always follow **all** matching edges when multiple edges had conditions that evaluated to true. This new feature allows you to choose between two strategies:

1. **All Matching Edges** (`all`) - Follow all edges that match (default behavior)
2. **First Matching Edge** (`first`) - Follow only the first edge that matches

## Configuration

Add the `edge_matching_strategy` field to any step:

```yaml
steps:
  - name: "Decision Step"
    activity: "some_activity"
    edge_matching_strategy: "first"  # or "all" (default)
    next:
      - step: "Path A"
        condition: "state.value > 10"
      - step: "Path B"
        condition: "state.value < 20"
```

## Go API

```go
step := &workflow.Step{
    Name:                 "Decision Step",
    Activity:             "some_activity",
    EdgeMatchingStrategy: workflow.EdgeMatchingFirst, // or workflow.EdgeMatchingAll
    Next: []*workflow.Edge{
        {Step: "Path A", Condition: "state.value > 10"},
        {Step: "Path B", Condition: "state.value < 20"},
    },
}
```

## Use Cases

### Parallel Processing (EdgeMatchingAll)
Perfect for scenarios where you want multiple operations to happen simultaneously.

### Priority-Based Decision Tree (EdgeMatchingFirst)
Ideal for routing based on priority where order matters.

## Examples

See the complete working example in `examples/edge_matching/main.go` which demonstrates both strategies in action.

## API Reference

### Constants
- `workflow.EdgeMatchingAll` - Follow all matching edges
- `workflow.EdgeMatchingFirst` - Follow first matching edge only

### Methods
- `Step.GetEdgeMatchingStrategy()` - Returns the strategy, defaulting to `EdgeMatchingAll`
