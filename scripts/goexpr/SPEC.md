# goexpr language specification

`goexpr` is a small expression language used by the workflow engine for
conditions, templates, and parameter interpolation. Source text is parsed
with Go's `go/parser.ParseExpr`, then walked directly — there is no
bytecode. This document is the authoritative description of what goexpr
accepts and what each construct means. Behavior outside this document
should be treated as an accident, not a guarantee.

The language borrows Go's expression syntax but is not Go. When we diverge
from Go semantics we call it out explicitly.

## Scope

goexpr evaluates **expressions**. There are no statements, blocks,
assignments, loops, declarations, or function literals. The grammar
accepted is exactly the subset of `ast.Expr` listed in
[Supported syntax](#supported-syntax).

## Lexical elements

### Integer literals

- Decimal (`42`), hex (`0xFF`, `0Xff`), octal (`0o17`, `0O17`), binary
  (`0b1010`, `0B1010`), and underscore-separated digits (`1_000_000`).
- All integer literals become `int64`. Values outside the `int64` range
  return an `ErrEvaluate` at Run time, not at Compile time — the parser
  accepts them, but evaluation fails.

### Floating-point literals

- Standard Go floats (`3.14`, `.5`, `1e6`, `1E-9`, `0x1p-10`). All float
  literals become `float64`.

### Character literals

- Single-quoted runes (`'a'`, `'\n'`, `'\u00e9'`). A rune literal
  evaluates to its Unicode code point as an `int64`, matching Go. Multi-
  rune or empty char literals return `ErrEvaluate`.

### String literals

- Double-quoted strings and raw backtick strings (`"hello"`,
  `` `hello` ``), with all Go escape sequences inside double-quoted form.

### Boolean and nil literals

- `true`, `false`, `nil` are reserved identifiers. They are not
  shadowable — even if `env` has a key named `true`, it is not reachable.

### Imaginary literals

- Rejected at evaluation with `ErrEvaluate`. goexpr does not model
  complex numbers.

## Operators

Precedence and associativity come from `go/parser`. They match Go:

| Precedence | Operators                      | Associativity |
| ---------- | ------------------------------ | ------------- |
| 5 (high)   | `*  /  %`                      | left          |
| 4          | `+  -`                         | left          |
| 3          | `==  !=  <  <=  >  >=`         | left          |
| 2          | `&&`                           | left          |
| 1 (low)    | `\|\|`                         | left          |

Unary `!`, `-`, `+` bind tighter than any binary operator. Parentheses
group expressions as usual. Go's bitwise operators (`&`, `|`, `^`, `<<`,
`>>`, `&^`) are parsed but not implemented — they return `ErrEvaluate`.

### Arithmetic (`+ - * / %`)

- Both operands `int64` → `int64` result. `+` on two strings is
  concatenation. Integer `/` and `%` by zero return `ErrEvaluate`.
- Any mix of int and float promotes both to `float64`. `%` on floats
  uses `math.Mod`. Float `/` and `%` by zero return `ErrEvaluate`.
- Integer overflow wraps (matching Go). `-MinInt64` and `MinInt64 / -1`
  wrap silently to `MinInt64`; they do not panic.
- `+` on any other type combination is an error.

### Comparison (`== != < <= >= >`)

- String vs string uses `strings` comparison.
- Numeric comparisons work across any combination of integer kinds and
  floats (see [Equality](#equality)).
- Other comparable Go types use native equality when both sides are the
  same type. Mismatched-but-comparable types yield `false` without an
  error. Uncomparable types (slices, maps, funcs) return `ErrEvaluate`.

### Logical (`&& ||`)

- Short-circuit. `false && X` evaluates to `false` without evaluating
  `X`; `true || X` evaluates to `true` without evaluating `X`.
- Both operands are run through [truthiness](#truthiness) rules first,
  so `"x" && 1` is `true`.
- The result type is always `bool`.

### Unary

- `!x` is logical negation using truthiness (so `!0` is `true`).
- `-x` negates a numeric value; any other type is an error.
- `+x` is a numeric no-op; any other type is an error.

## Equality

`==` and `!=` use a **loose** comparison:

1. `nil == nil` is `true`.
2. `X == nil` is `true` if `X` is a typed nil value of a nilable kind
   (chan, func, interface, map, pointer, slice). This means a `(*T)(nil)`
   or `[]any(nil)` stored in `any` compares equal to the literal `nil`.
3. If both sides are any combination of integer or float kinds, they
   convert to `float64` and compare. `int32(7) == int64(7) == float64(7)`
   is `true`.
4. Otherwise: if both runtime types are comparable, Go's `==` is used.
   Different types compare as `false` (no error). Uncomparable types
   return `ErrEvaluate`.

`!=` is exactly `!(==)`.

## Truthiness

Used by `!`, `&&`, `||`, `bool(v)`, and by workflow conditions. Delegated
to the engine-neutral `script.IsTruthyValue`, which treats these as
**falsey**:

- `nil`
- `false`
- Zero numeric values of any integer or float kind
- Empty string
- Empty slice, array, or map
- A nil channel, function, interface, map, pointer, or slice

Everything else is truthy.

## Identifier resolution

A bare identifier `foo` is resolved in this order:

1. The literals `true`, `false`, `nil`.
2. The `env` argument:
   - If `env` is `nil`, skip.
   - If `env` is `map[string]any`, look up `env["foo"]`.
   - If `env` is any other map with string keys, look up via reflection.
   - If `env` is a struct or a pointer to a struct, take the exported
     field named `foo`; if no field matches, take the bound method
     named `foo`. **Fields beat methods.**
3. The engine's registered functions (builtins plus anything passed to
   `WithFunctions`).
4. Otherwise: `ErrEvaluate: undefined identifier`.

Unexported struct fields are **not** reachable by name. Attempting to
select one returns `ErrEvaluate: field ... not found` — we deliberately
do not panic.

The names `state` and `inputs` are reserved by the workflow engine, so
`WithFunctions` panics if you try to register a function with either
name.

## Selectors (`x.y`)

`x.y` evaluates `x`, then looks up `y` on the result:

- Nil receiver → `ErrEvaluate`.
- `map[string]any` or any map with string keys → `y` is a key. Missing
  keys return an error (not a zero value).
- Map with non-string keys → `ErrEvaluate`.
- Struct → the exported field `y`, or `ErrEvaluate` if missing.
- Pointer to struct → dereferenced and re-tried. Nil pointer →
  `ErrEvaluate`.
- Anything else → `ErrEvaluate`.

Selectors chain left-to-right: `a.b.c` is `(a.b).c`. Selector chains are
limited by [evaluation depth](#limits-and-safety).

## Index expressions (`x[i]`)

- `map[string]any`: `i` must be a string. Missing key → `ErrEvaluate`.
- Other maps: `i` is converted to the map's key type if assignable or
  convertible. A nil index on a typed map is an error (not a panic).
- Slice, array: `i` must be an integer. Negative indices and indices
  `>= len(x)` return `ErrEvaluate` (goexpr does not support Python-style
  negative indexing).
- String: `i` selects the `i`-th **rune** (Unicode code point) and
  returns it as a one-rune string. `len(s)` is also in runes, so indexing
  and length stay consistent for non-ASCII strings.
- Anything else → `ErrEvaluate`.

Slice expressions (`x[a:b]`), full slices (`x[a:b:c]`), and type
assertions (`x.(T)`) are rejected.

## Calls (`f(a, b, ...)`)

### Call targets

The callable is resolved in order:

1. If the target is a bare identifier, `lookupEnv` runs, then the
   engine's functions.
2. If the target is a selector `x.f`, goexpr evaluates `x` and then
   looks for a method, struct field, or map entry named `f` on it.
3. Any other call target (index expression, call expression, paren
   expression) returns `ErrEvaluate: unsupported call target`.

### Method resolution order (for selector calls)

Given `x.f()` where `x` evaluates to `recv`:

1. If `recv` is `map[string]any`, the entry `recv["f"]` is used. Missing
   → error.
2. Else, `reflect.Value.MethodByName("f")` on the pointer or original
   receiver (so pointer-receiver methods are visible).
3. Else, if the dereferenced kind is `Struct`, the exported field `f`
   (as a function value).
4. Else, if the dereferenced kind is a `Map` with string keys, the entry
   `recv["f"]`.
5. Else: `ErrEvaluate: method ... not found`.

Nil pointer receivers produce `ErrEvaluate: cannot call ... on nil pointer`
before any reflect call that would panic.

### Argument handling

- Each argument is evaluated left to right. There is no support for the
  `...` spread syntax (passing a slice as variadic args).
- Non-variadic functions must receive exactly `NumIn()` arguments.
- Variadic functions accept `len(args) >= NumIn()-1`.
- goexpr represents ints as `int64` and floats as `float64`. It performs
  **range-checked** conversion to the declared parameter type. For
  example, `int64(10)` → `int8` succeeds; `int64(300)` → `int8` is an
  error (not a silent wraparound). Negative → unsigned fails.
  `float64`↔`float32` is allowed and may lose precision.
- Nil may be passed for any nilable-kind parameter (interface, pointer,
  map, slice, chan, func). Passing nil to a non-nilable parameter is an
  error.
- Any other conversion uses `reflect.Value.ConvertibleTo` + `Convert`.
  A nil function value (`var fn func(); fn == nil`) is detected and
  reported; it is never invoked.

### Return signatures

Supported:

- `func(...)` → result is `nil`.
- `func(...) T` → result is `T`.
- `func(...) (T, error)` → result is `T`; if the error is non-nil it
  replaces the normal result.

Anything else (two non-error returns, three returns, `(error, T)`)
returns `ErrEvaluate`. Errors from functions propagate wrapped inside
an `ErrEvaluate` chain via `errors.Is`.

## Builtins

All builtins can be disabled with `WithoutBuiltins()`. They are:

| Name            | Signature                       | Notes |
| --------------- | ------------------------------- | ----- |
| `len(v)`        | `(any) -> int, error`           | Rune count for strings, element count for slice/array/map/chan, `0` for nil, error otherwise. |
| `string(v)`     | `(any) -> string`               | Passthrough for strings, `fmt.Sprintf("%v", v)` otherwise, `""` for nil. |
| `int(v)`        | `(any) -> int64, error`         | Numeric values convert (float truncates toward zero). Strings are parsed strictly with `strconv.ParseInt` base-10 (trimmed whitespace, no `0x`, no trailing garbage). |
| `float(v)`      | `(any) -> float64, error`       | Like `int`, but `strconv.ParseFloat` 64-bit. |
| `bool(v)`       | `(any) -> bool`                 | Same semantics as [truthiness](#truthiness). |
| `contains(h,n)` | `(any, any) -> bool, error`     | Substring for string haystacks, element membership for slices/arrays (using [loose equality](#equality)), key presence for string-keyed maps. |
| `has(m,k)`      | `(any, string) -> bool, error`  | True if map `m` has key `k`. Maps only. Nil → `false`. |
| `keys(m)`       | `(any) -> []any, error`         | Sorted string keys. Other key types → error. |
| `lower(s)`      | `(string) -> string`            | `strings.ToLower`. |
| `upper(s)`      | `(string) -> string`            | `strings.ToUpper`. |
| `sprintf(f,...)`| `(string, ...any) -> string`    | `fmt.Sprintf`. |

## Higher-order special forms

goexpr also provides a fixed set of **special forms** for iterating
lists. These look like ordinary function calls in source, but the
second argument — the predicate — is not evaluated eagerly. Instead,
the form re-evaluates the predicate AST once per element with two
extra identifiers in scope:

- `it` — the current element
- `index` — the 0-based position as an `int64`

Inside the predicate, `it` and `index` shadow any identifier of the
same name from the outer env. Nested forms nest naturally:
`map(matrix, map(it, it * 10))` binds the inner `it` to each inner
element and the outer `it` is no longer reachable until the inner
`map` returns.

| Name                 | Returns                    | Description |
| -------------------- | -------------------------- | ----------- |
| `map(list, expr)`    | `[]any`                    | New list with `expr` evaluated per element. |
| `filter(list, pred)` | `[]any`                    | Elements where `pred` is truthy, in original order. |
| `any(list, pred)`    | `bool`                     | `true` if `pred` is truthy for any element; short-circuits. |
| `all(list, pred)`    | `bool`                     | `true` if `pred` is truthy for every element; short-circuits. Empty list → `true`. |
| `find(list, pred)`   | element or `nil`           | First element for which `pred` is truthy, or `nil`. |
| `count(list, pred)`  | `int64`                    | Number of elements for which `pred` is truthy. |

The `list` argument must be a slice or array (or `nil`, which is
treated as empty). Maps are not iterated by these forms — use
`keys(m)` to drive a map iteration manually.

Special-form names can be shadowed: if `WithFunctions` registers a
function with the same name, or the caller's env contains an entry
with that name, the user binding wins. This lets consumers replace
the built-in behavior when they need to. The `map` keyword is
special because Go's parser reserves it: goexpr rewrites `map` to an
internal token before parsing so the form can still be called as
`map(xs, it * 2)`, and translates it back for error messages and
method lookups.

## Helpful errors

goexpr annotates "not found" errors with a short hint drawn from the
names actually in scope:

- `undefined identifier "usernmae" (did you mean "username"?)`
- `field "Nmae" not found on User (did you mean "Name"?)`
- `key "naem" not found (did you mean "name"?)`

The hint is computed by Levenshtein distance against the set of
candidate names (env keys/fields/methods, registered functions, and
the higher-order form names). When there is no close match but the
candidate set is small enough to list compactly, the hint instead
lists the available names. When neither condition is useful, the
original error is returned unchanged so callers can still pattern-
match on it.

## Limits and safety

goexpr is meant to evaluate untrusted expression text without panicking.
The following are hard limits:

- **Max source length**: `MaxSourceLength` (64 KiB by default). `Compile`
  rejects longer input with `ErrCompile`.
- **Max evaluation depth**: `MaxEvalDepth` (256 by default). Expression
  trees deeper than this return `ErrEvaluate: expression nested too
  deeply`. This caps selector chains (`a.b.c...`), nested binary
  expressions, and nested calls.

Under adversarial input, goexpr must never:

- Panic (nil-deref, slice bounds, reflection on invalid values).
- Enter unbounded recursion.
- Silently produce out-of-range numeric conversions for call arguments.
- Expose unexported struct fields.

See `FuzzCompile` and `FuzzEval` for the enforcing test targets.

## Cancellation and termination guarantees

goexpr has no loop or recursion constructs of its own — `go/parser`
accepts only expressions, the evaluator makes strict downward progress
through the AST, and `MaxEvalDepth` caps the tree. Therefore a program
with no registered functions and no env-method calls has a hard
termination bound proportional to the AST size.

`Program.Run(ctx, env)` and `Engine.Eval(ctx, code, env)`
add cooperative cancellation on top of that bound:

- Every AST node visit checks `ctx.Err()` before dispatching. A
  cancelled or expired context causes the next node to return the raw
  `context.Canceled` / `context.DeadlineExceeded` error without wrapping
  it in `ErrEvaluate`, so callers can match with `errors.Is`.
- `Run` is the only evaluation entry point — there is no ctx-less form.
- Passing a nil `ctx` to `Run` falls back to `context.Background`.

**Automatic context injection for registered functions.** When a
function registered via `WithFunctions` declares `context.Context` as
its *first* parameter, goexpr passes the live context automatically.
The user-visible call surface excludes that parameter: arity checks,
argument positions, and error messages all refer to the caller's args.
Injection only fires when `context.Context` is the first parameter;
later positions are treated as ordinary arguments.

```go
e := goexpr.New(goexpr.WithFunctions(map[string]any{
    "fetch": func(ctx context.Context, url string) (string, error) { ... },
}))
// expression calls it as fetch("https://..."), the ctx from Run
// is threaded through automatically.
```

**Non-goal: forced termination of blocking user code.** Go provides no
mechanism to kill a goroutine. If a registered function or env method
ignores its context and blocks forever, goexpr cannot interrupt it —
that goroutine will not return until the user code chooses to. The
library deliberately does not wrap evaluation in a `select` on
`ctx.Done()` because early-returning the caller while the user code
keeps running would silently leak goroutines and hide real bugs in
caller code. Well-behaved integrations pass `context.Context` through
to any blocking call.

## Supported syntax

Only these `ast.Expr` node kinds are accepted; everything else returns
`ErrEvaluate: unsupported syntax ...`:

- `*ast.BasicLit` — literals
- `*ast.Ident` — identifiers
- `*ast.ParenExpr` — `( x )`
- `*ast.UnaryExpr` — `!x`, `-x`, `+x`
- `*ast.BinaryExpr` — arithmetic, comparison, logical
- `*ast.SelectorExpr` — `x.y`
- `*ast.IndexExpr` — `x[i]`
- `*ast.CallExpr` — `f(a, b, ...)`

Explicitly **not** supported (parses, but errors at Run time):

- Slice expressions (`x[a:b]`, `x[a:b:c]`)
- Type assertions (`x.(T)`)
- Composite literals (`[]int{1, 2}`, `T{...}`)
- Function literals (`func() {}`)
- Channel ops (`<-ch`, `ch <- v`)
- Pointer/address ops (`*x`, `&x`)
- Bitwise operators (`& | ^ << >> &^`)
- Imaginary number literals (`1i`)
- Spread call arguments (`f(xs...)`)
- Label and selector type names (`pkg.Type`)

## Error model

All runtime failures wrap `ErrEvaluate`; all parse failures wrap
`ErrCompile`. Callers check with `errors.Is(err, ErrEvaluate)` /
`errors.Is(err, ErrCompile)`. User function errors returned from
`(T, error)` signatures are wrapped such that both `errors.Is` and
`errors.As` still find the original cause.
