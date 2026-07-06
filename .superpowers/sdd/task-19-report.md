# Task 19 report: move idempotency sidecar-write into Reconcile

## Scope recap

Review of task 18 (real gRPC push, #20) flagged an idempotency gap: the
`<name>.json` sidecar `readExistingMetadata` uses to short-circuit a
genuine no-op reconcile was only ever written by `NewLocalPush` — the
Step 1 local-dev stand-in `PushFunc` — as an incidental side effect of
its own "write metadata to a file" implementation. `NewGRPCPush`, the
real push (now the production default, `--push-mode=grpc`), never
touched `jsonPath` at all. So under the production default, every single
reconcile event — any watch event, any resync — caused a full
render+build+push, even for a true repeat, with the agent's
`bootc_status` shell-out firing every time on the other end. Not a
correctness bug (the agent's own `apply()` is separately idempotent), but
indefinitely-recurring wasted work.

Fix: idempotency-tracking is `Reconcile`'s own concern, not something
each `PushFunc` implementation has to separately (and, as it turned out,
inconsistently) provide. Moved the sidecar write out of `NewLocalPush`
and into `Reconcile`, after a successful `r.Push` call.

## Exact change to `Reconcile`

`internal/operator/reconciler.go`, end of `Reconcile`, after the existing
`r.Push(ctx, meta, rawPath)` call:

```go
	if err := r.Push(ctx, meta, rawPath); err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: push: %w", err)
	}

	// Only now that Push has actually succeeded is it safe to record it:
	// writing the sidecar any earlier (or unconditionally, regardless of
	// Push's outcome) would make a future reconcile believe a failed push
	// already succeeded and wrongly skip retrying it -- a correctness bug
	// worse than the idempotency gap this sidecar-write closes. Same
	// MarshalOptions contract/fixtures/gen uses, so the sidecar format
	// matches the golden fixtures.
	data, err := (protojson.MarshalOptions{Multiline: true, Indent: "  "}).Marshal(meta)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: marshal metadata: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return ctrl.Result{}, fmt.Errorf("operator: write %s: %w", jsonPath, err)
	}

	return ctrl.Result{}, nil
}
```

`jsonPath` already existed as a local (`filepath.Join(r.OutputDir,
name+".raw")`'s sibling, computed earlier in `Reconcile` for
`readExistingMetadata`'s lookup) — no new path construction needed, just
reused it. The `protojson.MarshalOptions{Multiline: true, Indent: "  "}`
call is copied verbatim from `NewLocalPush`'s prior code (matching
`contract/fixtures/gen`'s marshal call, per the task's instruction not to
invent a different one).

## Exact change to `NewLocalPush`

Removed the marshal+`os.WriteFile` entirely; it now just logs:

```go
func NewLocalPush(outputDir, agentAddr string) PushFunc {
	return func(ctx context.Context, meta *desiredpb.DesiredMetadata, rawPath string) error {
		log.FromContext(ctx).Info("would push to agent (Step 1 stand-in: no network call made)",
			"agentAddr", agentAddr, "outputDir", outputDir, "name", meta.Name, "rawPath", rawPath)
		return nil
	}
}
```

Signature (`func NewLocalPush(outputDir, agentAddr string) PushFunc`) is
unchanged, per the task's constraint. `outputDir` is no longer used to
write anything — it's documentation-only now (kept in the log line for
context; the doc comment explains it names the same directory
`Reconciler.OutputDir` already is in practice, since `Reconcile` writes
both `<name>.raw` and `<name>.json` there itself regardless of which
`PushFunc` is wired in). `PushFunc`'s type doc comment, `NewLocalPush`'s
own doc comment, `Reconcile`'s doc comment, and the package doc comment
were all updated to stop attributing the sidecar write to `NewLocalPush`
and instead describe it as `Reconcile`'s own responsibility — these were
directly inaccurate after the code change, not adjacent cleanup.

## How I verified the failure-ordering safety property

Read the new code path with the failure case specifically in mind: the
sidecar-write block sits textually and causally *after* the
`if err := r.Push(...); err != nil { return ... }` early return, so any
non-nil error from `Push` returns before the marshal/write ever runs —
there is no path in `Reconcile` that reaches the write with a failed
push. Then wrote
`TestReconcile_DoesNotWriteSidecarOnPushFailure` (see below) as an
executable check of exactly that: reconcile once with a `PushFunc` that
always errors, assert (a) `Reconcile` returns an error and (b) no
`<name>.json` exists afterwards (`os.Stat` + `os.IsNotExist`), then swap
in a succeeding `PushFunc` and reconcile again with the *unchanged* same
input, asserting the second attempt still actually calls `Push` (not
skipped as a false no-op) — the two-sided version of the property: a
failed push must never be recorded as done, and a subsequent real attempt
on the same input must not be starved by a wrongly-written sidecar. Ran
this under `go test -race`; passes.

## Test changes

- **`TestNewLocalPush_WritesProtojsonSidecar` → renamed/repurposed to
  `TestNewLocalPush_DoesNotWriteSidecar`.** This test called the
  `PushFunc` `NewLocalPush` returns directly (never going through
  `Reconcile`) and asserted the sidecar landed on disk. That behavior no
  longer exists — `NewLocalPush` deliberately doesn't write it anymore —
  so the old test would now fail by construction. Rather than just
  deleting it, I repurposed it to assert the new, opposite contract: that
  calling `NewLocalPush`'s `PushFunc` does *not* create `<name>.json`.
  This isn't a no-op rename — it's a regression guard against the
  duplicate-write side effect creeping back in (which would reintroduce
  a second, uncoordinated sidecar-writer and reopen exactly the kind of
  inconsistency this task fixes).
- **`TestReconcile_IdempotentOnUnchangedInput`** (uses real `NewLocalPush`
  through `Reconcile`, unchanged in behavior/assertions) — still passes.
  Verified it now exercises the new path: first reconcile has no
  existing sidecar, builds/pushes, and `Reconcile` itself writes
  `<name>.json` after `NewLocalPush`'s `PushFunc` (which no longer writes
  anything) returns nil; second reconcile finds that sidecar with a
  matching digest and returns via the early "already up to date" branch
  without touching the file again — so `firstWrite == secondWrite`
  because the file is untouched, not because two independent writers
  coincidentally produced identical bytes. Updated the inline comment,
  which previously (incorrectly, post-fix) attributed the sidecar write
  to `NewLocalPush`.
- **`TestReconcile_DetectsDigestChange`** (uses `recordingPush`, a stub
  that never wrote a sidecar even before this fix) — still passes,
  unchanged. Worth noting for the record: under the *old* code this test
  passed regardless of whether the idempotency check worked at all,
  because `recordingPush` never wrote a sidecar, so `readExistingMetadata`
  always missed and every call fell through to build+push — coincidentally
  matching this test's "push must fire again on digest change" assertion
  either way. Under the *new* code it's actually meaningful: the first
  reconcile's `Push` succeeding causes `Reconcile` to write the sidecar
  with `testDigest1`; the second reconcile (after the ConfigMap is
  updated to `testDigest2`) finds that sidecar but its digest doesn't
  match, so it correctly falls through to build+push again rather than
  being skipped.
- **New: `TestReconcile_IdempotentAcrossPushModes`** — the actual
  regression guard for the bug this task fixes. Uses `recordingPush` (a
  fake that behaves like `NewGRPCPush`: records the call, returns nil,
  never touches `outputDir`) and reconciles twice with the *same*
  ConfigMap content. Asserts `push.calls` is 1 after the first reconcile
  and *still* 1 after the second — i.e. the second reconcile was actually
  skipped as a no-op. This is the test that would have failed before this
  fix (under the old code, with a `PushFunc` that never wrote a sidecar,
  the second call would also have hit `push.calls == 2`).
- **New: `failingPush` fake + `TestReconcile_DoesNotWriteSidecarOnPushFailure`**
  — the failure-ordering safety-property test described above.

`internal/operator/grpc_push_test.go` was not changed: it tests
`NewGRPCPush` directly (never through `Reconcile`), and `NewGRPCPush` never
wrote a sidecar before or after this fix — nothing in that file's
assertions changed.

## Test output

`mise exec -- go build ./...` — clean, no output.

`mise exec -- go vet ./...` — clean, no output.

`mise exec -- gofmt -l internal/operator` — clean, no output (no files
listed).

`mise exec -- go test -race -count=1 ./internal/operator/... ./cmd/nanokube-operator/... -v`:

```
=== RUN   TestNewGRPCPush_SuccessSendsExactDesired
--- PASS: TestNewGRPCPush_SuccessSendsExactDesired (0.00s)
=== RUN   TestNewGRPCPush_AgentErrorSurfaces
--- PASS: TestNewGRPCPush_AgentErrorSurfaces (0.00s)
=== RUN   TestNewGRPCPush_MissingBlobSkipsPushWithoutError
--- PASS: TestNewGRPCPush_MissingBlobSkipsPushWithoutError (0.00s)
=== RUN   TestReconcile_ConfigMapNotFound
--- PASS: TestReconcile_ConfigMapNotFound (0.40s)
=== RUN   TestReconcile_IgnoresUnrelatedObject
--- PASS: TestReconcile_IgnoresUnrelatedObject (0.00s)
=== RUN   TestReconcile_NoDigestKey
--- PASS: TestReconcile_NoDigestKey (0.01s)
=== RUN   TestReconcile_RendersAndPushes
--- PASS: TestReconcile_RendersAndPushes (0.02s)
=== RUN   TestReconcile_IdempotentOnUnchangedInput
--- PASS: TestReconcile_IdempotentOnUnchangedInput (0.03s)
=== RUN   TestReconcile_DetectsDigestChange
--- PASS: TestReconcile_DetectsDigestChange (0.03s)
=== RUN   TestReconcile_ReusesExistingBlobWithoutRebuilding
--- PASS: TestReconcile_ReusesExistingBlobWithoutRebuilding (0.02s)
=== RUN   TestNewLocalPush_DoesNotWriteSidecar
--- PASS: TestNewLocalPush_DoesNotWriteSidecar (0.00s)
=== RUN   TestReconcile_IdempotentAcrossPushModes
--- PASS: TestReconcile_IdempotentAcrossPushModes (0.03s)
=== RUN   TestReconcile_DoesNotWriteSidecarOnPushFailure
--- PASS: TestReconcile_DoesNotWriteSidecarOnPushFailure (0.04s)
=== RUN   TestIsBuildToolMissing
=== RUN   TestIsBuildToolMissing/systemd-repart_not_found
=== RUN   TestIsBuildToolMissing/mkfs.erofs_message
=== RUN   TestIsBuildToolMissing/unrelated_failure
--- PASS: TestIsBuildToolMissing (0.00s)
    --- PASS: TestIsBuildToolMissing/systemd-repart_not_found (0.00s)
    --- PASS: TestIsBuildToolMissing/mkfs.erofs_message (0.00s)
    --- PASS: TestIsBuildToolMissing/unrelated_failure (0.00s)
PASS
ok  	github.com/MatchaScript/nanokube/internal/operator	1.645s
?   	github.com/MatchaScript/nanokube/cmd/nanokube-operator	[no test files]
```

(Log lines interleaved with `--- PASS` above are elided here for
brevity; the run this session showed no unexpected warnings — the
"WARNING: DDI build tooling unavailable" lines are expected on this dev
host, which lacks `mkfs.erofs`, matching every prior task's test output.)

Full `go test` was also covered by `go build ./...`/`go vet ./...`
across the whole module (not scoped to `internal/operator`), both clean.

## Files changed

- `internal/operator/reconciler.go` — sidecar write moved from
  `NewLocalPush` into `Reconcile` (after a successful `r.Push`); doc
  comments (package, `PushFunc`, `NewLocalPush`, `Reconcile`) updated to
  match.
- `internal/operator/reconciler_test.go` — `TestNewLocalPush_WritesProtojsonSidecar`
  repurposed into `TestNewLocalPush_DoesNotWriteSidecar`; inline comment
  in `TestReconcile_IdempotentOnUnchangedInput` corrected; two new tests
  added (`TestReconcile_IdempotentAcrossPushModes`,
  `TestReconcile_DoesNotWriteSidecarOnPushFailure`) plus the `failingPush`
  fake they share; removed now-unused `protojson` import.
- `internal/operator/grpc_push_test.go` — not touched (see above).

Not touched: `agent/`, `contract/`, `internal/render`, `internal/ddi`,
`cmd/nanokube-operator/main.go`. On `main.go`: its `--output-dir` flag
help text ("local directory the push stand-in writes
`<name>.raw`/`<name>.json` into (--push-mode=local)") is now slightly
imprecise — `Reconcile` writes both files itself in both push modes, not
just the "stand-in" in local mode (and, for `<name>.raw`, this was
already true before this task's fix too, since `Reconcile` always builds
the DDI regardless of `PushFunc`). Left it alone: it's a flag-help
wording nit, not a functional issue, and the task's constraint was to
leave `main.go` untouched barring a genuinely necessary change — this
isn't one.

## Self-review findings

- Verified `jsonPath` reused in the new write is the exact same variable
  `readExistingMetadata(jsonPath)` reads earlier in `Reconcile` — no risk
  of a path mismatch between the read side and the new write side.
- Verified `NewLocalPush`'s `outputDir` parameter, now otherwise unused
  in the function body, doesn't trigger any vet/lint failure (Go doesn't
  flag unused *parameters*, only unused locals/imports) — confirmed via
  the clean `go vet`/`gofmt` runs above — and kept it referenced in the
  log line so it's not silently dead.
- Double-checked `NewGRPCPush` (`internal/operator/grpc_push.go`) was not
  touched and never wrote a sidecar in the first place — its own doc
  comment's reference to "mirrors NewLocalPush, which also never fails
  on a missing rawPath" is about the *missing-blob-skip* behavior, not
  the sidecar, so it remains accurate and didn't need updating.
- Confirmed no other call sites of `NewLocalPush` exist besides
  `cmd/nanokube-operator/main.go` (`--push-mode=local`) and the two test
  files — grepped the whole repo.
- Confirmed the `recordingPush` fake used across multiple tests already
  matched exactly what the task asked for in step 4's third bullet (a
  fake that behaves like `NewGRPCPush` — never writes a sidecar) — no
  need to invent a second, separate fake for that purpose.
- One prior test (`TestReconcile_IdempotentOnUnchangedInput`) is weaker
  than it looks: it would have passed even before this fix, because
  `NewLocalPush`'s old sidecar-write was deterministic and content-only
  compared (`firstWrite == secondWrite` as byte strings), not a call-count
  assertion. I did not strengthen it further since
  `TestReconcile_IdempotentAcrossPushModes` (new) is the test that
  actually catches the regression via call-count; flagging this instead
  of silently leaving the impression that both tests are equally strong
  guards.

## Commit

Command: `git commit -m "fix(operator): move idempotency sidecar-write into Reconcile (was only incidental via NewLocalPush)"`

Files staged: `internal/operator/reconciler.go`,
`internal/operator/reconciler_test.go`.

(Commit SHA and worktree branch name recorded by the calling task; see
its final report for the exact hash — this file was written just before
committing.)
