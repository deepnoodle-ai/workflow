# Bounded paths in loops that fork

**Status:** Implemented in #76, in review
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
  rejoin, or end without rejoining (beside the join, or on a `when` before
  it), or the loop forks without a join.
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
- Bounding a loop with no join on the way back whose returning branch
  changes from pass to pass. The graph cannot tell which of two
  conditional branches comes back, so each change moves the loop to a
  child path, and records and IDs grow by one per change. A join on the
  way back that names the returning branch avoids it (section 1). When
  the returning branch does not change, the loop moves once and stays
  (preference 0, added for #78).
- Counting paths in `MaxStateBytes`. Once paths are bounded by live work,
  the count adds nothing a user can act on.
- Reading states that this branch's predecessors wrote. Nothing with this
  state format has been released.

## Proposal

### 1. A fork continues on its path

When more than one edge matches, the path continues along one target, and
each other target starts a child path, as before. The path no longer ends
as `forked`. It continues along the branch most likely to carry the loop
back, in this order of preference (`carrier`):

0. The target the path left this fork along last time, if it matched
   (`pathState.Via`, added for #78). The path is back, so that branch
   returns. A child starts with the target it was started for. Without
   this, a loop with no join on the way back, whose first branch that can
   return ends on a `when` or comes back only through a `catch` route,
   moved to a child path on every pass: 201 records at 200 passes.
1. The first target whose branch a join on the way back names: before
   the branch returns to the fork, it reaches a join that can reach the
   fork and names a step on the branch (`computeNamed`, at Load). Load
   checks that a named step settles on an arriving path whenever the join
   fires, except across loop back edges (`joincheck.go`), so that branch
   usually comes back through the join.
2. Else the first target from which the forking step can be reached again.
3. Else the first target (the fork is not in a loop).

"First" is among the targets that matched on this pass, in the order the
definition lists them: `switch` targets, then `next` edges. When conditions
change between passes, the branch that stays can change too. The choice
uses the graph, the matched targets and the path's own history only
(`def.reach`, `def.named`, `Via`), never event order.
Preference 1 matters when the first returning branch can end on a `when`
before the join, as an audit or notify step does. Preference 2 alone would
move the loop to a child path on every pass there.

This changes no value. A child path starts as a copy of its parent
(`Visits`, `Scope`, `Settled`), so the branch that stays on the parent
reads and records exactly what a child would. Only its ID differs:
`main/tick` instead of `main.tick/tick`.

In the #48 loop, `poll` on `main` continues to `tick`, and `note` and
`tock` start `main.note` and `main.tock`. In the fork-only loop, `poll`
continues to `wait`, so the loop stays on `main`.

### 2. A join continues on one of its arrivals

A join already continues on its deepest common ancestor when that is one
of the arrivals. Otherwise it continued on the ancestor when the arrivals
"covered" every branch of the ancestor's last fork, and on a new path
otherwise. With section 1, a forking path is always one of its own
branches, so:

- If every branch of a fork arrives, the forking path is among them, and
  the join continues on it, as `covers` did.
- If only part of a fork arrives, the forking path arrives or it does
  not. If it does, the join continues on it. If it does not, the join
  continues on the arrival with the lowest ID, in string order.

So the rule becomes: **the join continues on the arrival that is an
ancestor of all the others, if there is one; otherwise on the arrival
with the lowest ID, in string order** (`main.a~10` before `main.a~2`). A
join never starts a path. `covers`, `cover`, `pathState.Children`,
`pathState.Absorbed` and the `forked` end go away. An ended path never
becomes active again.

The merged path keeps the visit counts of every arrival (the highest of
each), as it did before, so its later visits are numbered past anything
any arrival ran, and its StepIDs stay unique. Two joins never share a
path, because each continues on one of its own arrivals, and a path
arrives in one firing only. The choice depends only on the arrivals.

A join must not start a path under the forking path. That path is now
live, and it may fork again to the same segment (a fork target can be the
join itself). The join's allocation and the fork's would then take
numbers from one counter in an order that depends on event order.

In the #48 loop, `j` merges `main` (at `tick`) and `main.tock`. `main` is
an ancestor of `main.tock`, so the run continues on `main`. `note` ending
on its own no longer matters.

### 3. IDs come from a counter on the parent

`pathState` gains `Seq map[string]int` (JSON `seq`): how many child paths
the path has started per segment. `allocPath(p, seg)` increments
`p.Seq[seg]` and returns `p.ID + "." + seg`, with `~N` when N > 1. Only `p`
allocates IDs that start with `p.ID + "."` (a segment is a step name, so it
has no `.` or `~`). Only forks allocate (section 2), and a path forks only
in its own transitions, so its IDs are allocated in the order those
happen. So:

- The IDs are what probing gave as long as nothing was dropped
  (`main.tick~2` on the second pass), and they do not depend on event
  order.
- An ID is never handed out twice, even after its record is dropped: the
  parent's counter remembers it. A dropped parent never allocates again,
  because it has ended for good (section 2), and its own ID is never
  reused, by induction from the roots.

The number of entries in `Seq` is bounded by the number of steps. Their
values count a path's forks across passes, so they grow, like a visit
number, but only in digits.

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
`ErrUnknownStep`. This is more lenient than today's check on a live path,
which also rejects a visit above the path's visit count.

The visit number under a dropped path is not checked: the path's visit
counts went with its record. So a StepID with a visit the run never issued
on that path is ignored too. This leniency is deliberate. A host sends only
StepIDs it got from commands, and `ErrUnknownStep` exists to catch host
bugs, not to audit visit numbers. The tests pin only visits that were
issued, so a later change may tighten it.

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
- **Start a new path at a partial join** (`<ancestor>.<join>`), as before.
  The ancestor is now the live forking path, and it can fork to the same
  segment, so the two allocations share one counter in event order. A
  review found this with a fork target that is the join itself. Rejected:
  continue on an arrival (section 2).
- **Stay on the first branch that can return** (preference 2 alone). An
  audit branch that ends on a `when` before the join moved the loop to a
  child path on every pass: 101 records and an 818-character StepID at 100
  passes. Rejected: prefer the branch a join on the way back names.

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

One implementation PR, stacked on this one, closing #48. It adds:

- A regression test over four loop shapes: the #48 repro, a branch that
  ends on a `when` before the join, the fork-only loop, and a full join.
  Over many passes, the path count stays constant, and the StepIDs grow
  only in their counters.
- Tests that read a step only a dropped path ran, through the outputs, a
  `finally` step and an `on_failure` step (`retired`).
- An order test of a partial join beside a forking path that keeps
  running. `property_test.go`'s generator can now link a fork branch
  straight to its join, so random graphs cover that shape too.
- A check, after every Apply in `fakeHost`, that every ID the state names
  is in the state and that every live path's lineage reaches `main`.

No migration. Durable hosts store the state as a value and are
unaffected.

## Open questions

- None blocking. The remaining growing shape is a loop whose returning
  branch keeps changing (Non-goals).
