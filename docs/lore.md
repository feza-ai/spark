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

## Buildah/OCI image builds need `securityContext.privileged: true`

A pod running `buildah build`/`buildah push` fails under Spark's default
(non-privileged) pod securityContext: it can't do the overlay mount work
buildah needs ("failed to make mount private: permission denied").
Switching to `--storage-driver vfs` does not dodge this -- it fails later,
applying layers, with "remount /: permission denied". The actual fix is
`privileged: true` in the container spec, kept on the default overlay
storage driver.

Confirmed 2026-09-02 building `canary-sire:latest` for ECR (adapting
`jobs/dgx-canary/build-images.sh`'s pattern from the DGX local registry to
ECR): the first two submissions failed exactly this way, back to back; the
third, adding `privileged: true` and reverting to overlay, succeeded in
~2 minutes.

**How to apply:** any manifest submitting a buildah/image-build job to
Spark needs `privileged: true` set explicitly -- don't copy a non-privileged
pod spec as a starting point for an image build. This is a real widening
of what the pod is granted, so call it out in the manifest/PR rather than
adding it silently.
