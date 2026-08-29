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
