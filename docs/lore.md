# Lore: invariants and landmines

Non-obvious traps discovered while working in this repo. Grep this file for
the area you're about to change before debugging it.

## internal/manifest: same-indent list items silently parse as empty

The hand-rolled YAML parser (`internal/manifest/yaml.go`) does not handle a
block sequence indented at the *same* column as its parent mapping key --
the common, valid YAML style used by `kubectl` examples and by this repo's
own `testPodYAML` fixture (`internal/api/pods_mutate_test.go`):

```yaml
spec:
  containers:
  - name: main       # same indent as "containers:" -- silently parses to an empty list
    image: alpine:latest
```

It does handle the deeper-indent style used throughout
`internal/manifest/*_test.go` (e.g. `TestParse_ValidPod`):

```yaml
spec:
  containers:
    - name: main     # one level deeper than "containers:" -- parses correctly
      image: alpine:latest
```

No error is raised either way -- `containers` is just silently `nil`.
`TestApplyPod` (`internal/api/pods_mutate_test.go`) uses the broken style but
never asserts on container contents, so it's never caught this.

**Why it matters:** any new test manifest (or hand-written fixture YAML)
that uses same-indent list items will parse pod/container fields as empty
without erroring, producing a confusing "field is empty" failure that looks
like a bug in whatever endpoint/handler is under test rather than in the
fixture. Discovered while writing `internal/api/pods_manifest_test.go`
(issue #80 T4.7/T4.8): a same-indent `containers:` fixture produced zero
containers.

**How to apply:** always write new test manifest YAML with list items
indented one level deeper than their parent key (`internal/manifest/parse_test.go`'s
style), not same-indent. The same-indent case is itself a real parser bug
(already the confirmed root cause of issue #77, fixed in PR #90) -- it's
recorded here for the general class, not because this repo is still
carrying it.

## GitHub auto-closes an issue twice: once from the feature PR, again from the release-please PR

A merged PR whose body contains `Fixes #N`/`Closes #N` auto-closes the
issue -- expected, and why this repo's Operating Procedure requires
reopening it if the fix isn't live-verified yet (see `docs/plan.md`).
Less obvious: **the *next* release-please PR can independently re-close
the same issue a second time**, even after you've reopened it.

release-please builds its changelog from each commit's conventional-commit
body, and if the original commit used a `closes #N`/`fixes #N` trailer
(common when a commit message documents what it fixes for the changelog),
that trailer's text survives verbatim into the release PR's auto-generated
body (`chore(main): release X.Y.Z`). GitHub's auto-close scanner reads
*any* merged PR body for that keyword, not just the originating feature
PR -- so merging the release PR fires it again, closing the issue at the
exact same timestamp as the release merge.

Confirmed 2026-08-29: PR #101 (issue #71) and PR #90 (issue #77) were both
reopened after their own merges auto-closed them; both were silently
re-closed a second time the moment release-please's PR #91 merged
(`closedAt` on both issues == PR #91's `mergedAt`, to the second).

**How to apply:** after merging a release-please PR, re-check every issue
this wave touched (`gh issue view <N> --json state,closedAt`), not just
the ones you expect -- a `closedAt` matching the release PR's merge time
is the tell. There is no reliable way to prevent this from the commit
side (the trailer is what makes release-please's changelog useful) --
treat "reopen after merge" as needing a second pass after the release PR
merges too, not a one-time step.

## internal/scheduler: available.memoryMB reads full precisely when the node is full

`GET /api/v1/resources` reports
`available.memoryMB = allocatable - sum(declared memory requests)`. Nothing
in that figure comes from the host. A container that declares neither a
memory request nor a memory limit was accounted at 0MB, so a node packed
with such containers reported its memory ledger as **completely empty while
it was full** -- and admission, which enforces that ledger strictly and
correctly, found room.

Measured on a GB10 (114372MB allocatable) before the incident, not inferred
from it: with one 8GiB job running, `allocated.memoryMB` read 8192 and the
cgroup confirmed `memory.max = 8589934592`; deleting it moved
`allocated.memoryMB` to 0. Eight CI containers, mid-build, declared 0MB
between them. A 100GiB render was then admitted against a ledger reporting
the full 114372MB free, and minutes later the host stopped servicing the
network at the link layer (issue #121).

**Why it matters:** *any runbook that gates on `available.memoryMB` passes
in exactly the situation it exists to catch.* A rule of the form "only
submit if available memory is above N" is a statement about bookkeeping, not
about RAM.

**How to apply:** gate on live `/proc/meminfo` `MemAvailable`, not on
`/api/v1/resources`. In-process, that is what
`Scheduler.SetHostMemory`/`--live-memory-guard` now does, and
`spark_live_memory_refusals_total` counts how often the host overrules the
ledger. `--default-memory-request-mb` makes the ledger pessimistic rather
than blind, but its accuracy is coincidental: it charges undeclared
containers a fixed guess, so whether it is sufficient depends on how many
are resident. At the incident's 8 containers a 2048MB default would have
refused the render; at 5 it would have admitted it. Treat
`spark_defaulted_memory_admissions_total` climbing as a prompt to fix the
manifests, not as the problem being solved. See
`docs/adr/015-undeclared-memory-admission.md`.

## internal/manifest: silent zero is this repo's recurring defect class

Four separate issues, one shape: input that should have been a resource
quantity became `0` with no error, and admission trusted it.

- **#43** -- a limits-only pod was accounted at zero (`requests` absent).
  Fixed by Kubernetes' own rule: an unspecified request defaults to the
  limit (`parseResources`, `job.go`).
- **#66** -- flow-style YAML maps (`limits: { memory: 512Mi }`) parsed as a
  scalar string, so `getMap` returned nil and the container was accounted at
  zero.
- **#77** -- block sequences at the same indent as their key parsed as an
  empty list.
- **#121** -- a container declaring neither requests nor limits was
  accounted at zero.

**How to apply:** when touching the parser, the question is never "does this
parse" but "what does this produce when it does not parse". The rule the
repo settled on: **unparseable input must be an error; absent input may be
defaulted, but never to zero on a dimension admission enforces.** Defaulting
must also run *after* parsing, never inside it, or a malformed quantity
quietly becomes the default -- which would convert #66 from a loud bug into
a silent one.
