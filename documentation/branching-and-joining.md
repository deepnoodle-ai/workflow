# Branching and Joining

Workflows execute as a directed graph. When a step has multiple matching
outgoing edges, the engine creates parallel branches that run concurrently.
Join steps bring branches back together.

## How branching works

Every step has a `Next` field with outgoing edges. Each edge can have a
`Condition` (an expression that must evaluate to true) and a `BranchName`:

```go
{
    Name:     "Route",
    Activity: "classify",
    Store:    "classification",
    Next: []*workflow.Edge{
        {Step: "Process A", BranchName: "a", Condition: `state.classification == "typeA"`},
        {Step: "Process B", BranchName: "b", Condition: `state.classification == "typeB"`},
        {Step: "Process C"},  // unconditional, continues on current branch
    },
}
```

When multiple edges match:
- Each matching edge creates a new branch running in its own goroutine.
- Each child branch receives a **deep copy** of the parent's state.
- After branching, branches are fully independent — no shared mutable state.

Named branches (via `BranchName`) are important for two reasons:
1. They can be referenced by join steps.
2. They can be referenced by workflow outputs.

## Edge matching strategies

By default (`EdgeMatchingAll`), all matching edges create branches. Use
`EdgeMatchingFirst` to follow only the first matching edge — useful for
if/else-if/else patterns:

```go
{
    Name:                 "Decision",
    Activity:             "evaluate",
    EdgeMatchingStrategy: workflow.EdgeMatchingFirst,
    Next: []*workflow.Edge{
        {Step: "High",    Condition: "state.score > 80"},
        {Step: "Medium",  Condition: "state.score > 50"},
        {Step: "Low"},    // default: matches when nothing above does
    },
}
```

See [Edge Matching Strategies](edge-matching-strategies.md) for more detail.

## Conditional branching

Conditions are expressions evaluated by the configured script engine. They
have access to `state.*` (branch variables) and `inputs.*` (workflow inputs):

```go
Next: []*workflow.Edge{
    {Step: "Handle Prime",     Condition: `state.is_prime == true && state.category == "small"`},
    {Step: "Handle Composite", Condition: `state.is_prime == false`},
}
```

String literals in conditions must be double-quoted — the expression engine
follows Go lexical rules.

## Fan-out: parallel branches

Create named parallel branches that you'll join later:

```go
{
    Name:     "Start",
    Activity: "setup",
    Store:    "initial_value",
    Next: []*workflow.Edge{
        {Step: "Process A", BranchName: "a"},
        {Step: "Process B", BranchName: "b"},
        {Step: "Wait For Results", BranchName: "final"},
    },
}
```

This creates three branches:
- Branch `a` runs `Process A`
- Branch `b` runs `Process B`
- Branch `final` runs `Wait For Results` (which will join the other two)

Each branch starts with a copy of the parent's state, including
`initial_value`.

## Joining branches

A join step waits for specified branches to complete, then merges selected
variables from those branches into its own state:

```go
{
    Name: "Wait For Results",
    Join: &workflow.JoinConfig{
        Branches: []string{"a", "b"},
        BranchMappings: map[string]string{
            "a.result_a": "valueA",   // extract a specific variable
            "b.result_b": "valueB",
            // "a": "all_of_a"        // or store the entire branch state
        },
    },
    Next: []*workflow.Edge{{Step: "Combine"}},
}
```

### JoinConfig fields

| Field | Description |
|-------|-------------|
| `Branches` | Names of branches to wait for |
| `Count` | Number of branches to wait for (0 = all listed branches) |
| `BranchMappings` | How to extract data from completed branches |

### BranchMappings

Mappings use dot notation:

```go
BranchMappings: map[string]string{
    "a.result":     "result_from_a",  // state variable "result" from branch "a"
    "b.data.count": "b_count",        // nested field extraction
    "a":            "branch_a_state", // entire branch state as a map
}
```

The left side is `branchName.variableName` (source). The right side is the
variable name in the joining branch (destination).

### Partial joins

Set `Count` to join after a subset of branches complete:

```go
Join: &workflow.JoinConfig{
    Branches: []string{"a", "b", "c"},
    Count:    2,  // proceed after any 2 of the 3 complete
    BranchMappings: map[string]string{
        "a.result": "resultA",
        "b.result": "resultB",
        "c.result": "resultC",
    },
}
```

Variables from branches that haven't completed yet will not be available
in the mappings.

## Each loops (fan-out over a collection)

The `Each` field on a step iterates over a collection, creating a sub-branch
for each item:

```go
{
    Name:     "Process Items",
    Activity: "process_item",
    Each: &workflow.Each{
        Items: "${state.items}",  // expression that evaluates to a slice
        As:    "item",            // variable name for the current item
    },
    Store: "processed",
    Next:  []*workflow.Edge{{Step: "Done"}},
}
```

For each element in the evaluated `Items`:
1. A fresh sub-branch is created with a copy of the parent's state.
2. The current element is set as the variable named by `As`.
3. The activity executes with access to that variable.

`Items` can be a slice, array, map, or scalar. The `script.EachValue`
helper handles the conversion internally. Maps iterate over values.

## Complete fan-out/fan-in example

```go
wf, _ := workflow.New(workflow.Options{
    Name: "parallel-pipeline",
    Steps: []*workflow.Step{
        {
            Name:     "Setup",
            Activity: "setup_data",
            Store:    "initial_value",
            Next: []*workflow.Edge{
                {Step: "Process A", BranchName: "a"},
                {Step: "Process B", BranchName: "b"},
                {Step: "Join", BranchName: "final"},
            },
        },
        {
            Name:     "Process A",
            Activity: "work_a",
            Store:    "result_a",
            // terminal: no Next edges
        },
        {
            Name:     "Process B",
            Activity: "work_b",
            Store:    "result_b",
            // terminal: no Next edges
        },
        {
            Name: "Join",
            Join: &workflow.JoinConfig{
                Branches: []string{"a", "b"},
                BranchMappings: map[string]string{
                    "a.result_a": "valueA",
                    "b.result_b": "valueB",
                },
            },
            Next: []*workflow.Edge{{Step: "Finalize"}},
        },
        {
            Name:     "Finalize",
            Activity: "combine",
            Store:    "final_result",
        },
    },
    Outputs: []*workflow.Output{
        {Name: "result", Variable: "final_result", Branch: "final"},
    },
})
```

This workflow:
1. Runs `Setup`, then fans out into three branches.
2. Branches `a` and `b` run their work in parallel.
3. Branch `final` hits the `Join` step and waits for `a` and `b` to complete.
4. After joining, it extracts `result_a` and `result_b` into its own state.
5. The `Finalize` step combines the results.
6. The workflow output is extracted from branch `final`.

## State isolation

After branching, each branch has its own copy of all state variables.
Changes in one branch are invisible to others. This eliminates race
conditions and makes the concurrency model straightforward.

```go
// Branch "a" sets result_a = 200
// Branch "b" sets result_b = 300
// Neither sees the other's writes
// The join step explicitly pulls the values it needs via BranchMappings
```

If you need data from parallel branches, you must join them. There is no
way to read another branch's state directly.
