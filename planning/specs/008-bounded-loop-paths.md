# Bounded paths in loops that fork

**Status:** Implemented in #77
**Author:** Curtis Myzie
**Date:** 2026-10-29
**Workflow:** Spec, then build on a branch stacked on this one.
**Issue:** #48

## Context

`definitions.md` promises that a run's state is bounded by its latest
passes, "so a loop whose values fit twice under the cap can run
indefinitely: a poll and `sleep` loop that runs for months holds one
poll's output". Step visits keep that promise: `collect` drops every visit
a later one superseded. Paths do not. The doc names this as the one
exception ("a loop that forks and joins on every pass adds a small record
per branch per pass"), but the exception is wider than it says:

| Loop (12 passes) | Path records | Longest path ID |
| --- | --- | --- |
| fork, join two branches, a third branch ends (#48) | 49 | `main.j.j.j.j.j.j.j.j.j.j.j.tock` |
| fork, one branch ends, the other loops (no join) | 23 | `main.wait.wait.wait.wait.wait.wait.wait.wait.wait.wait.wait` |
| fork, join every branch | 25 | `main.tock~10` |

Records grow in every shape. IDs grow in the first two, and every StepID
on pass N carries N copies of a segment. `MaxStateBytes` counts neither.
A sleep loop can run indefinitely, so neither has a limit.

Three mechanisms cause this:

1. **A fork ends its path.** Each target starts a child path named
   `<parent>.<target>`. A loop that forks every pass therefore moves to a
   new, longer path every pass, unless a join brings it back.
2. **A join continues on the forking path only when it accounts for every
   branch** (`covers`). A branch that ends without reaching the join, like
   the third branch in #48, makes the join start a new path,
   `<ancestor>.<join>`, under the previous pass's path.
3. **Nothing drops a path record.** `allocPath` finds a free ID by probing
   for records, so dropping records would reuse IDs and break the
   uniqueness of `StepID.Unit()`, which postgres side-effect idempotency
   depends on.

Nothing has been released since v0.0.5, before the engine in #42. StepIDs
embed path IDs and hosts persist them, so this is the time to change how
paths are named.

## Goals

- A loop that runs indefinitely keeps a constant number of path records,
  and its StepIDs stop growing, in these shapes: each pass's branches
  rejoin, or end without rejoining, or the loop forks without a join.
  StepIDs grow only by the digits of a counter, as `@<visit>` already
  does.
- StepIDs stay unique within a run, including across dropped records.
- The result depends only on the graph and the arrivals, never on event
  order. `property_test.go` and `soundness_fuzz_test.go` stay green.
- Values are unchanged: every expression, join check, output and
  `on_failure`/`finally` scope reads what it reads today. Only path IDs
  change.
- A late result for an attempt on a dropped path is ignored like any late
  result, not reported as `ErrUnknownStep`.

## Non-goals

- Bounding a loop whose number of live paths grows every pass (a branch
  that loops back by itself while another branch also continues the
  loop). That run's live work grows without limit anyway.
- Bounding a loop that each pass continues on a path that a partial join
  created, while a branch that can still reach a join stays out. Records
  stay bounded by the live paths and their ancestors, but IDs still nest.
  Nothing in the examples or tests has this shape. A later issue can
  address it if a workflow needs it.
- Counting paths in `MaxStateBytes`. Once paths are bounded by live work,
  the count adds nothing a user can act on.
- Reading states that this branch's predecessors wrote. Nothing with this
  state format has been released.

## Proposal

### 1. A fork continues on its path

When more than one edge matches, the path continues along one target, and
each other target starts a child path, as before. The path no longer ends
as `forked`. It continues along the **first target, in edge order, from
which the forking step can be reached again**: the branch that carries
the loop. If no target can (the fork is not in a loop), it continues
along the first target. The choice uses `def.reach` only, so it depends
on the graph.

This changes no value. A child path starts as a copy of its parent
(`Visits`, `Scope`, `Settled`), so the branch that stays on the parent
reads and records exactly what a child would. Only its ID differs:
`main/tick` instead of `main.tick/tick`.

In the #48 loop, `poll` on `main` continues to `tick`, and `note` and
`tock` start `main.note` and `main.tock`. In the fork-only loop, `poll`
continues to `wait`, so the loop stays on `main`.

### 2. A join continues on an arrival, or on a new path

A join already continues on its deepest common ancestor when that is one
of the arrivals. Otherwise it continued on the ancestor when the arrivals
"covered" every branch of the ancestor's last fork, and on a new path
otherwise. With section 1, a forking path is always one of its own
branches, so:

- If every branch of a fork arrives, the forking path is among them, and
  the join continues on it, as `covers` did.
- If only part of a fork arrives, the forking path arrives or it does
  not. If it does, the join continues on it. If it does not, the join
  starts a new path, as before.

So the rule becomes: **the join continues on the arrival that is an
ancestor of all the others, if there is one; otherwise on a new path under
their deepest common ancestor.** `covers`, `cover`, `pathState.Children`,
`pathState.Absorbed` and the `forked` end go away. An ended path never
becomes active again.

Two joins that each merge part of one fork still never share a path: at
most one of them has the forking path among its arrivals, and the other
starts a new path. The choice depends only on the arrivals, as before.

In the #48 loop, `j` merges `main` (at `tick`) and `main.tock`. `main` is
an ancestor of `main.tock`, so the run continues on `main`. `note` ending
on its own no longer matters.

### 3. IDs come from a counter on the parent

`pathState` gains `Seq map[string]int` (JSON `seq`): how many child paths
the path has started per segment. `allocPath(p, seg)` increments
`p.Seq[seg]` and returns `p.ID + "." + seg`, with `~N` when N > 1. Only `p`
allocates IDs that start with `p.ID + "."` (a segment is a step name, so it
has no `.` or `~`), and a path's IDs are allocated in the order its own
transitions happen. So:

- The IDs are what probing gave as long as nothing was dropped
  (`main.tick~2` on the second pass), and they do not depend on event
  order.
- An ID is never handed out twice, even after its record is dropped: the
  parent's counter remembers it. A dropped parent never allocates again,
  because it has ended for good (section 2), and its own ID is never
  reused, by induction from the roots.

`Seq` is bounded by the number of steps per path.

With sections 1 to 3, the #48 loop runs on `main`, and pass N's branches
are `main.note~N` and `main.tock~N`.

### 4. Dropping path records

At the end of each `Apply`, after `collect`, the engine drops every path
record that nothing can read again. It keeps:

- the roots `main`, `failure` and `finally`;
- every path that has not ended (active, or waiting at a join);
- every ancestor of such a path, by `Parent`. `commonAncestor` walks these
  for the arrivals of a future join, and every future path descends from
  a live one;
- every ended path whose `Node` is set. `collect` keeps an ended path's
  current node only while it is its step's latest visit, or while the
  default outputs read it (`endNodes`).

Anything else has ended and has no node. No future join walks it, and no
output reads it, except through its scope.

**Scopes.** Outputs, `on_failure` and `finally` read the union of every
path's scope (`unionScope`, `conclude`), so an ended path's scope still
matters. Before a path is dropped, the entries in its scope that are the
latest visit of their step are merged into a new state-level map,
`retired` (JSON `retired`), with `mergeScope`. `unionScope`, `conclude`
and `collect`'s `top` read `retired` as one more scope. `collect` keeps
the nodes `retired` names and removes any entry that a later visit has
superseded. So `retired` holds at most one entry per step, and the union
is the same as before the drop.

**References.** A kept path whose `Parent` was dropped gets `Parent: ""`.
Only live paths and their ancestors are walked, and a dropped path is
neither, so this changes no result. It keeps the invariant that every path
ID the state names is in the state. A test checks it.

**Why this does not depend on event order.** Each condition goes one way
only: a path never revives, a `Node` is only ever cleared, and a path
stops being an ancestor of a live path but never starts again. So a path
that can be dropped at some Apply can be dropped at every later one, and
the final state's records are the same whatever the order. The scope
entries merged into `retired` are the latest visits of their step. The
latest visit of a step only moves forward, so an entry that is still the
latest at the end was the latest whenever its path was dropped. `retired`
therefore ends as the same function of the final state.

`collect` already skips its work under the test-only `keepVisits` option.
Dropping paths skips under it too, so that option still shows the full
history.

### 5. Late results on dropped paths

`Apply` ignores a result for a visit that `State.dropped` recognizes as
superseded, and returns `ErrUnknownStep` for anything else it cannot find.
`dropped` reads the path record. With section 4 a record can be gone, so
`dropped` gains a fallback for a path ID with no record:

- Find the longest prefix of the ID that names a kept path K. A root is
  always kept, so one exists.
- The next segment, `name` or `name~N`, must have been allocated:
  `N <= K.Seq[name]`, where `name` alone means N = 1.
- Each later segment must have the shape `step` or `step~N` for a step in
  the definition. Their owners are gone, so their counters cannot be
  checked.
- The step part of the node key must name a step, as it does today.

A StepID that passes is ignored as late. One that fails is still
`ErrUnknownStep`. This is as lenient as today's check, which already
"cannot tell such a visit from one its lineage made on an ancestor path".

### 6. MaxStateBytes

Paths stay out of `MaxStateBytes`. After this change a run holds the live
paths, their ancestors, one ended path per step's latest visit (more when
parallel paths tie), and `retired`. All of that is bounded by the graph
and the live work, not by the number of passes.

### 7. Docs

- `definitions.md`, "Step IDs": a fork continues on its path along one
  branch, and the other branches get child paths, `main.tests`, with
  `~2` on a later pass.
- `definitions.md`, "Paths and joins": the new fork and join rules
  (sections 1 and 2).
- `definitions.md`, the "One exception remains" paragraph: path records
  and IDs are bounded too, with the remaining shape from Non-goals.
- `hosts.md` and `llms.txt` show `main.tests/lint@2#1`, which is still a
  valid ID. Check that the surrounding text does not describe the old
  fork rule.

## Alternatives considered

- **Keep `covers`, and exempt branches that can never reach a join.** This
  fixes #48's IDs, because `note` stops blocking the join. But the
  fork-only loop still nests (`main.wait.wait`), and `covers` still needs
  the fork tree kept. Rejected: section 1 fixes both and deletes code.
- **Collapse a repeated segment in an ID** (`main.j.j` becomes `main.j~2`).
  The counter for the collapsed name has to live on a path that may
  already be gone. A run-wide counter gives numbers that depend on the
  order of events. When two parallel lineages share the collapsed prefix,
  a suffix taken from visit or pass numbers can collide. Rejected.
- **A run-wide sequence number in every path ID.** Unique and bounded, but
  the numbers depend on which branch allocates first, so dispatches would
  differ by event order. Rejected.
- **Keep every record, shorten only the IDs.** It does not meet the goal:
  the state still grows by a record per branch per pass.

## Tradeoffs and consequences

- Path IDs change wherever a fork happens: one branch keeps the parent's
  ID. Tests that assert IDs change. Hosts treat StepIDs as opaque, and
  nothing has been released.
- `State` JSON loses `children` and `absorbed` per path, and gains `seq`
  per path and `retired` per state. The state is bookkeeping, not API.
- `ErrUnknownStep` accepts a StepID on a path that was allocated and
  dropped. It still rejects a path that was never allocated under a kept
  path.

## Rollout

One implementation PR, stacked on this one, closing #48. It adds a
regression test from the #48 repro and the fork-only loop: over many
passes, the path count stays constant and the longest StepID grows only
with its counters. It also adds a check, after every Apply in `fakeHost`,
that every ID the state names is in the state. No migration. Durable
hosts store the state as a value and are unaffected.

## Open questions

- None blocking. The non-goal shape (a loop carried by a partial join)
  can get its own issue if a workflow needs it.
