# expr examples

Small, runnable programs showing how the `expr` expression language
looks and works. Each subdirectory is a standalone `main.go`; run any of
them with `go run ./examples/expr/<name>`.

| Directory          | What it shows                                            |
| ------------------ | -------------------------------------------------------- |
| `basic/`           | literals, arithmetic, comparisons, logical ops, map env  |
| `structs/`         | struct envs with fields and bound methods                |
| `funcs/`           | registering custom Go functions as callable identifiers  |
| `higher_order/`    | `map` / `filter` / `any` / `all` / `find` / `count`      |
| `compile_once/`    | compile once, evaluate many — the hot-path pattern       |
| `workflow/`        | plugging expr into the workflow engine as a compiler     |

`expr` lives at [github.com/deepnoodle-ai/expr](https://github.com/deepnoodle-ai/expr).
It is a zero-dependency evaluator that accepts the subset of Go
expression syntax useful for workflow conditions, templates, and
parameter interpolation. It has no statements — every program is a
single expression that returns a value.
