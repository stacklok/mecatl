# path-escape-posture — plan index

Wave 1 of the path-escape posture relax (docs/acceptance/path-escape-posture.md).
Scope: Scenarios 1–3 + 5 (the classifier, the `auto`/`yolo` allow path, and the
child/Glob-Grep confinement guards). Scenario 4 (the `strict`/`trusted` ask path)
+ guardrail routing are Wave 2.

## Tasks

- [01-escape-classifier](tasks/01-escape-classifier.md) — the composition escape
  classifier (no behaviour change). Satisfies AC1.1–AC1.5.
- [02-relaxed-read](tasks/02-relaxed-read.md) — `yolo`/`auto` allow out-of-root
  reads; pseudo-fs deny; os.Root containment; restart rehydration. Satisfies
  AC2.1–AC2.7. Blocked by 01.
- [03-relaxed-write](tasks/03-relaxed-write.md) — `yolo` allow writes, `auto` ask;
  os.Root-served; Edit-ledger canonical keys; deny-dominance. Satisfies
  AC3.1–AC3.6. Blocked by 02.
- [04-child-and-glob-confinement](tasks/04-child-and-glob-confinement.md) — child
  engines never relax; Glob/Grep confined at every posture. Satisfies
  AC5.1–AC5.4. Blocked by 02.

## Dependency graph

```
01-escape-classifier
   └── 02-relaxed-read
         ├── 03-relaxed-write
         └── 04-child-and-glob-confinement
```

Wave 1: [01]. Wave 2: [02]. Wave 3: [03, 04] (parallel).
