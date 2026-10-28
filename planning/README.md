# Planning

Design notes, kept for their reasoning. The code and the guides in
`documentation/` are the reference; where a note disagrees, the code wins.

Current, about the engine merged in #42:

- [proto/progression-engine-summary.md](proto/progression-engine-summary.md):
  a shareable overview of `Apply`, the host contract, typed definitions and
  the two hosts.
- [proto/flow-progression-findings.md](proto/flow-progression-findings.md):
  what the spike that built it found.
- [proto/typed-definitions.md](proto/typed-definitions.md): how expressions,
  routing and data are typed.

Historical, about the goroutine engine before #42 (`v0.0.5`): everything in
[review/](review), [prds/](prds) and [specs/](specs). They describe
activities, checkpointers and signals, which no longer exist.
