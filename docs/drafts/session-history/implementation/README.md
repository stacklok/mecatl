# Implementation reasoning

These reports explain the design choices behind the session-history kernel:
what establishes authority, how accepted facts are represented, who owns values,
and where execution enforcement meets replaceable capability behavior. They are
design material, not documentation of shipped runtime or storage behavior. The
[session-history review map](../README.md) owns the domain contracts.

## Authority and history representation

- [Execution kernel and capability boundaries](kernel.md) explains acceptance
  gates, admission, reconstruction, runtime isolation, and history identity.
- [Content-addressed history format](history-format.md) explains commit grouping,
  root binding, canonical identity bytes, physical storage freedom, and the
  alternatives considered.

## Representation ownership and codec choice

- [Snapshots, ownership, and cloning](ownership.md) distinguishes stable views
  from read-only references and describes detached copies and publication.
- [Persistence codecs and DTO ownership](codecs-and-dtos.md) explains the public
  format boundary, handwritten Atmos codecs, strict validation, ownership,
  future generation tradeoffs, and the limits of the codec experiments.

## Execution and extensions

- [Extensions and execution policy boundaries](components.md) applies these
  contracts to delegation, learning, and compaction while keeping authority and
  lifecycle enforcement in the kernel.
