# AGENTS.md: kernel views

`views` is the kernel's public, stdlib-only runtime for read-only borrowed
containers. Follow [`../AGENTS.md`](../AGENTS.md) for the applicable kernel
contract, documentation, ownership, and focused-testing standards.

Keep views independent of identity, codecs, storage, and sessions. Document
borrowing, retained backing, immutability, and lifetime on every public
container or constructor; a view never freezes data or synchronizes access.
