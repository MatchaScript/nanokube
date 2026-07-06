# Task 18 report: real gRPC push to nanokube-agent (replaces local-write stand-in)

## Scope recap

Implementation plan item 5 (実装項目5) closeout: swap the operator's
`PushFunc` seam (`internal/operator/reconciler.go`) from the Step 1
local-write stand-in (`NewLocalPush`) to a real dev-grade (plaintext)
gRPC call to `nanokube-agent`'s `Agent.PushDesired` — and demonstrate the
whole loop for real, for the first time this session: operator (on Kind)
detects a ConfigMap change, renders, (attempts to) build, and pushes over
real gRPC to a real `nanokube-agent` process, which verifies and applies
it.

## `NewGRPCPush` design

New file `internal/operator/grpc_push.go`:

```go
func NewGRPCPush(agentAddr string) PushFunc {
	return func(ctx context.Context, meta *desiredpb.DesiredMetadata, rawPath string) error {
		logger := log.FromContext(ctx)

		blob, err := os.ReadFile(rawPath)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Info("skipping push: no DDI blob was built for this document (build tooling unavailable on this host), nothing to push", ...)
				return nil
			}
			return fmt.Errorf("operator: read blob %s: %w", rawPath, err)
		}

		conn, err := grpc.NewClient(agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return fmt.Errorf("operator: dial agent %s: %w", agentAddr, err)
		}
		defer conn.Close()

		client := desiredpb.NewAgentClient(conn)
		resp, err := client.PushDesired(ctx, &desiredpb.Desired{
			Name: meta.Name, TargetImageDigest: meta.TargetImageDigest,
			BlobSha256: meta.BlobSha256, Blob: blob,
		})
		if err != nil {
			return fmt.Errorf("operator: PushDesired to %s: %w", agentAddr, err)
		}
		logger.Info("pushed to agent", "agentAddr", agentAddr, "desiredName", resp.GetDesiredName())
		return nil
	}
}
```

- Dials `agentAddr` fresh on every call (`grpc.NewClient` +
  `insecure.NewCredentials()`, plaintext — Step 1 has no TLS; mTLS is
  Step 5) and closes the connection via `defer conn.Close()` before
  returning. Per-call dial+close, no pooling — matches the task's "don't
  over-engineer" guidance for how infrequently a reconcile actually
  pushes.
- On gRPC error, wraps with `fmt.Errorf("... %w", err)` so
  `status.Code(err)`/`status.FromError(err)` still work on the caller
  side (verified by `TestNewGRPCPush_AgentErrorSurfaces`) and
  `Reconcile` treats it as an ordinary reconcile error (retried by
  controller-runtime) — no special-casing added, matching the task's
  explicit instruction.
- On success, logs `resp.GetDesiredName()`.

### The blob-missing decision

The task offered two explicit options for when `rawPath` doesn't exist
(the build-tool-missing sentinel case from `Reconcile`): skip the push
with a clear log, or push anyway and let the agent's checksum
verification reject it. **I chose skip**, via `os.IsNotExist(err)` on the
`os.ReadFile(rawPath)` result (checking file existence directly, not the
`buildSkippedSentinel` string constant — the two are equivalent given how
`Reconcile` drives `PushFunc`, but checking existence keeps `NewGRPCPush`
decoupled from that specific sentinel and matches the task's own phrasing
literally: "if the file doesn't exist ... skip").

Why skip over push-and-reject: the sentinel case means *this host*
already knows its own DDI build tooling is missing (logged by `Reconcile`
itself, one line above). Pushing anyway would spend a real network
round-trip and produce a remote `InvalidArgument: blob checksum mismatch`
that reads like a transport/data bug, when the actual cause (no build
tool here) is already known and already logged locally. Skipping with a
clear log message keeps that known environment gap from ever surfacing as
a reconcile error that controller-runtime would retry forever, and
mirrors `NewLocalPush`'s own behavior (it also never fails on a missing
`rawPath` — it just never reads it). Any other read error (e.g.
permission denied) is *not* silently swallowed: it returns a real Go
error, since that's an unexpected problem, not the known sentinel case.

This decision only affects the *local* dial-vs-skip choice; it does not
change what the agent does when it *does* receive a bad blob (the Rust
side's own checksum verification, unmodified, still rejects those calls —
see `agent/src/server.rs`'s `push_desired_checksum_mismatch_...` test,
unaffected by this task).

### `cmd/nanokube-operator` wiring

Added `--push-mode=grpc|local` (default `grpc`) to
`cmd/nanokube-operator/main.go`. `grpc` calls `operator.NewGRPCPush(agentAddr)`
(new production default); `local` calls the unchanged
`operator.NewLocalPush(outputDir, agentAddr)` (kept for local dev without
a running agent, not deleted, per the task's explicit constraint). An
unrecognized value logs an error and exits 1 rather than silently falling
back to either mode.

Doc comments in `cmd/nanokube-operator/main.go`'s package comment and
`internal/operator/reconciler.go`'s package + `PushFunc` doc comments were
updated in place — they explicitly described the *old* stand-in-only
behavior ("stands in for the future gRPC push ... A later task ... swaps
in a PushFunc that dials --agent-addr") which is now stale/inaccurate
about the code those comments sit directly above; left everything else
in both files untouched.

## Test output

New file `internal/operator/grpc_push_test.go`: a `fakeAgentServer`
embeds `desiredpb.UnimplementedAgentServer` and implements `PushDesired`
to record the received `*desiredpb.Desired` and return either a canned
`PushDesiredResponse` or a canned gRPC error; served over a real
`net.Listen("tcp", "127.0.0.1:0")` + `grpc.NewServer()` (no bufconn — a
real loopback listener was simpler here and just as isolated).

Three tests:
- `TestNewGRPCPush_SuccessSendsExactDesired` — asserts the fake server
  received `Name`, `TargetImageDigest`, `BlobSha256` matching `meta`
  exactly, and `Blob` byte-for-byte equal to the file at `rawPath`.
- `TestNewGRPCPush_AgentErrorSurfaces` — fake server returns
  `status.Error(codes.InvalidArgument, ...)`; asserts
  `status.Code(err) == codes.InvalidArgument` on the error `NewGRPCPush`
  returns (proves the gRPC status survives the `fmt.Errorf` wrap).
- `TestNewGRPCPush_MissingBlobSkipsPushWithoutError` — `rawPath` points
  at a file that was never written; asserts `push(...)` returns `nil`
  *and* the fake server's `received` field stays `nil` (proves the skip
  happens before dialing, not just before erroring).

```
$ mise exec -- go build ./...
(exit 0, no output)

$ mise exec -- go vet ./...
(exit 0, no output)

$ mise exec -- gofmt -l internal/operator cmd/nanokube-operator
(exit 0, no output)

$ mise exec -- go test -race -count=1 ./internal/operator/... ./cmd/nanokube-operator/... -v
=== RUN   TestNewGRPCPush_SuccessSendsExactDesired
    ... INFO pushed to agent {"agentAddr": "127.0.0.1:44275", "desiredName": "v1-name"}
--- PASS: TestNewGRPCPush_SuccessSendsExactDesired (0.00s)
=== RUN   TestNewGRPCPush_AgentErrorSurfaces
--- PASS: TestNewGRPCPush_AgentErrorSurfaces (0.00s)
=== RUN   TestNewGRPCPush_MissingBlobSkipsPushWithoutError
    ... INFO skipping push: no DDI blob was built for this document (build tooling unavailable on this host), nothing to push {...}
--- PASS: TestNewGRPCPush_MissingBlobSkipsPushWithoutError (0.00s)
--- PASS: TestReconcile_ConfigMapNotFound (0.4x s)
--- PASS: TestReconcile_IgnoresUnrelatedObject
--- PASS: TestReconcile_NoDigestKey
--- PASS: TestReconcile_RendersAndPushes
--- PASS: TestReconcile_IdempotentOnUnchangedInput
--- PASS: TestReconcile_DetectsDigestChange
--- PASS: TestReconcile_ReusesExistingBlobWithoutRebuilding
--- PASS: TestNewLocalPush_WritesProtojsonSidecar
--- PASS: TestIsBuildToolMissing (+ 3 subtests)
PASS
ok  	github.com/MatchaScript/nanokube/internal/operator	1.6-1.7s
?   	github.com/MatchaScript/nanokube/cmd/nanokube-operator	[no test files]
```

All 12 tests pass, including the 9 pre-existing `internal/operator`
tests (unchanged, still green). This dev host has `systemd-repart` but
lacks `mkfs.erofs` — same as every earlier task's finding — so
`TestReconcile_RendersAndPushes` hits the documented sentinel branch, as
expected, not a regression.

## Real end-to-end demonstration

Environment: `docker info` confirmed no daemon socket on this host;
rootless `podman` (netavark backend, `rootlessNetworkCmd: pasta`) is
available and working. Used `KIND_EXPERIMENTAL_PROVIDER=podman`, matching
task 16's precedent.

### Building both binaries

```
$ cd agent && mise exec -- cargo build                 # unmodified agent/, built as-is
   ... Finished `dev` profile [unoptimized + debuginfo] target(s) in 9.19s

$ podman build -t localhost/nanokube-operator:step1 -f cmd/nanokube-operator/Dockerfile .
   ... Successfully tagged localhost/nanokube-operator:step1
```

### Running the real agent (writable temp dirs, not real `/var/lib/...`)

```
$ nohup mise exec -- ./target/debug/nanokube-agent serve \
    --listen 0.0.0.0:9090 \
    --confexts-dir <scratch>/agent-confexts \
    --bookkeeping-path <scratch>/agent-bookkeeping.json \
    > <scratch>/agent.log 2>&1 &

$ cat <scratch>/agent.log
nanokube-agent: listening on 0.0.0.0:9090 (plaintext, dev-grade)
```

### Networking: how the Kind pod reaches the host's agent process

Tried the podman-bridge-gateway approach first (`podman network inspect
kind` showed gateway `10.89.0.1`) — **did not work**: a container on the
`kind` network could not reach `10.89.0.1:9090` (`Connection refused`).
Root cause (confirmed, not guessed): this host's rootless podman uses
`pasta` as its rootless network backend, which does **not** expose the
bridge's own gateway address as a route to the real host; instead it
exposes a separate link-local address via the documented
`host.containers.internal` / `host.docker.internal` alias. Verified
directly:

```
$ podman run --rm --network kind ... fedora-minimal:latest -c 'cat /etc/hosts; ...'
169.254.1.2	host.containers.internal host.docker.internal
--- try gateway 10.89.0.1:9090 ---
FAIL: gateway not reachable
--- try host.containers.internal:9090 ---
OK: host.containers.internal reachable
```

Then verified the same raw IP (`169.254.1.2`) is reachable from **inside
a plain Kind pod** (no `hostNetwork: true` needed — kindnet's normal pod
egress masquerade through the node's own interface into the shared
`kind` bridge is sufficient; pod's own `/etc/hosts` doesn't have the
`host.containers.internal` name, so the raw IP is what a pod must use):

```
$ kubectl run nettest --image=fedora-minimal:latest -- sleep 3600
$ kubectl exec nettest -- bash -c 'echo > /dev/tcp/169.254.1.2/9090' && echo OK
OK: reachable
```

So the working approach was **neither of the two the task suggested**
(`hostNetwork: true`, or the bridge gateway IP) — it's podman/pasta's own
host-loopback address, discovered empirically. `cmd/nanokube-operator/deploy.yaml`'s
`--agent-addr` was updated to `169.254.1.2:9090` with a comment
explaining this is host- and provider-specific and needs rediscovering
elsewhere.

### Real Kind run

```
$ kind create cluster --name nanokube-e2e            # KIND_EXPERIMENTAL_PROVIDER=podman
 ✓ ... Thanks for using kind!

$ kind load docker-image localhost/nanokube-operator:step1 --name nanokube-e2e
$ kubectl apply -f cmd/nanokube-operator/deploy.yaml
serviceaccount/nanokube-operator created
clusterrole.rbac.authorization.k8s.io/nanokube-operator created
clusterrolebinding.rbac.authorization.k8s.io/nanokube-operator created
deployment.apps/nanokube-operator created
```

**Operator startup log** (confirms `pushMode: grpc` — the new default —
actually took effect):

```
... INFO setup starting nanokube-operator {"configmapName": "nanokube-desired-input", ..., "agentAddr": "169.254.1.2:9090", "pushMode": "grpc"}
... INFO controller-runtime.metrics Starting metrics server
... INFO Starting Controller {"controller": "nanokube-operator-configmap", ...}
... INFO Starting workers {..., "worker count": 1}
```

**First reconcile** (`kubectl create configmap nanokube-desired-input
--from-literal=targetImageDigest=sha256:1111...111a`): the operator's own
container image (by design, per task 16 — it deliberately omits
`systemd-repart`/`mkfs.erofs`) has no DDI build tooling, so `Reconcile`
hits the sentinel path, `rawPath` is never created, and `NewGRPCPush`
correctly skips the network call entirely (per this task's own chosen
design):

```
... INFO rendered desired document {"name": "51de7dd9...", "targetImageDigest": "sha256:1111...111a"}
... INFO WARNING: DDI build tooling unavailable, skipping build; writing sidecar with a sentinel blob_sha256 instead of a real one {..., "buildErr": "systemd-repart not found in PATH"}
... INFO skipping push: no DDI blob was built for this document (build tooling unavailable on this host), nothing to push {"name": "51de7dd9...", "rawPath": ".../51de7dd9....raw"}
```

Agent log: unchanged (no request arrived, as expected — this run
correctly demonstrates the "skip, don't push a doomed request" design,
but doesn't yet exercise the wire itself).

**To actually exercise the real gRPC call** (the point of this task) in
an environment where DDI building is impossible everywhere I can reach
(confirmed: this dev host itself also lacks `mkfs.erofs`, see the `go
test` output above — `internal/ddi`/`internal/render` are out of scope
for this task, so I did not try to work around that gap), I used
`Reconcile`'s own, already-tested blob-reuse path (exactly what
`TestReconcile_ReusesExistingBlobWithoutRebuilding` validates as
legitimate: if `<name>.raw` already exists, `Reconcile` reuses it and
recomputes its real sha256, rather than rebuilding). I pre-placed a
stand-in blob at the exact path `Reconcile` would look for
(`kubectl exec` into the running pod, `echo -n '...' > .../51de7dd9....raw`),
then triggered a fresh reconcile with `kubectl patch configmap ...
targetImageDigest=sha256:2222...`:

**Operator log** (this time the blob exists, so `NewGRPCPush` actually
dials and calls `PushDesired`):

```
... INFO rendered desired document {"name": "51de7dd9...", "targetImageDigest": "sha256:2222..."}
... INFO DDI already built for this name, reusing it {"name": "51de7dd9...", "rawPath": ".../51de7dd9....raw", "bytes": 46}
... ERROR Reconciler error {..., "error": "operator: push: operator: PushDesired to 169.254.1.2:9090: rpc error: code = Internal desc = systemd-confext refresh --mutable=yes: exit status: 1: Need to be privileged.\n"}
```

This is exactly the expected outcome the task anticipated: the gRPC call
reached the real agent process, the agent's checksum verification
**passed** (no `InvalidArgument`), `place()` **succeeded**, and the
failure that surfaced is the expected environment-limited one
(`systemd-confext` needing privilege) — not a wiring bug. Confirmed by
inspecting the agent's own state directly:

```
$ ls <scratch>/agent-confexts/
.generations  51de7dd9b12ffa23a9860204a44dcc046e209058361f7c1f2feb824012061b0e.raw

$ cat <scratch>/agent-confexts/51de7dd9....raw
stand-in confext DDI blob for task-18 e2e demo

$ cat <scratch>/agent-bookkeeping.json
cat: No such file or directory   # correctly absent: refresh() failed before write_bookkeeping() ran
```

The placed file's content matches exactly what was pushed over gRPC
(`place()` ran, wrote real bytes), and bookkeeping was correctly never
written (matches `pipeline::apply`'s call order: `place` → `refresh` →
`write_bookkeeping` — execution stopped at `refresh`, before
`write_bookkeeping`). `nanokube-agent`'s own stdout only logs at process
startup (no per-request tracing subscriber wired into `main.rs` — out of
scope to add), so the gRPC error message returned to the operator plus
this on-disk state together are the two independent confirmations that
the RPC actually reached and ran through the real `apply()` pipeline, not
a stub.

Controller-runtime's own retry (visible as a second, near-identical
`Reconciler error` entry ~2s later) is the expected behavior for a
reconcile that returned an error — not something this task needed to
handle specially, per the task's own instruction that no special-casing
belongs in `Reconcile`.

### Cleanup

```
$ kind delete cluster --name nanokube-e2e
Deleted nodes: ["nanokube-e2e-control-plane"]
$ kind get clusters
No kind clusters found.
$ podman rmi localhost/nanokube-operator:step1
Untagged: localhost/nanokube-operator:step1
Deleted: a27ebd0eef98...
$ podman images | grep nanokube-operator   # -> no output
$ pkill -f target/debug/nanokube-agent; ps aux | grep nanokube-agent   # -> no output, process stopped
$ rm -rf <scratch>/agent-confexts <scratch>/agent-bookkeeping.json <scratch>/agent.log
```

No Kind cluster, built operator image, or running agent process left
behind. The shared `kind` podman bridge network (a leftover from an
earlier task's session, not created by this task) was left alone — it's
kind's own convention to keep this network across cluster
create/delete cycles for reuse, not task-specific state.

## Files created/changed

- `internal/operator/grpc_push.go` — new, `NewGRPCPush`.
- `internal/operator/grpc_push_test.go` — new, its tests.
- `cmd/nanokube-operator/main.go` — `--push-mode` flag, wiring, updated
  package doc comment.
- `cmd/nanokube-operator/deploy.yaml` — `--agent-addr` updated to the
  empirically-discovered pasta host-loopback address, with a comment
  explaining it's host/provider-specific.
- `internal/operator/reconciler.go` — package doc comment and `PushFunc`
  doc comment updated (both explicitly described the now-superseded
  "stand-in only, a later task swaps this in" state); no logic changed.

Not touched: `agent/`, `contract/`, `internal/render`, `internal/ddi`,
`internal/operator/reconciler_test.go` (pre-existing tests, all still
green unmodified).

## Self-review findings

- **`NewGRPCPush`'s skip decision is keyed on file existence, not on
  reading `buildSkippedSentinel` directly** — deliberate, to keep the
  function decoupled from that specific internal constant; documented in
  the function's own doc comment. If `Reconcile` ever grows a second
  reason for `rawPath` to be legitimately absent, this still does the
  right thing without needing to track a new sentinel value.
- **Grpc-mode pushes never write the `<name>.json` sidecar** that
  `Reconcile`'s own idempotency check (`readExistingMetadata(jsonPath)`)
  reads — only `NewLocalPush` ever writes it. This means every reconcile
  event under `--push-mode=grpc` re-renders and re-attempts push, even
  for a genuine no-op repeat (never hits the "already up to date"
  skip). I did not fix this: the task's own framing ("`Reconciler.Reconcile`
  does not need to change for that swap") and the "don't modify
  `internal/render`, `internal/ddi`" / no `reconciler.go` logic-change
  instruction both point at this being accepted, pre-existing scope for
  this seam, not something task 18 asked me to close. Flagging it
  explicitly since it's a real behavioral gap a future task (or the CRD
  work in Step 4, which likely replaces this whole idempotency mechanism
  anyway) should account for — the agent's own `apply()` is still
  idempotent on its side (`bk.desired_name != desired.name` gates
  place/refresh), so this doesn't cause double-writes on the agent, just
  wasted reconciles/RPCs on the operator side.
- **The Kind-reachable agent address (`169.254.1.2:9090`) is
  environment-specific** (rootless podman + pasta on this exact host) —
  documented in `deploy.yaml` as such; a different provider/host would
  need to rediscover it the same empirical way this task did. This
  matches the task's own "Before You Begin" allowance for the Kind
  networking question to be genuinely environment-dependent.
- Considered testing the "operator has a real DDI build, agent's checksum
  verification runs against a genuinely computed sha256" path in the e2e
  demo too, but the actual result (pre-seeded stand-in blob, real sha256
  computed by `Reconcile`'s reuse path, real gRPC call, agent's real
  checksum check passing, `place()` succeeding, `refresh()` failing on
  privilege) already covers everything the task asked the e2e demo to
  show; did not also manufacture the checksum-mismatch/sentinel-rejected
  variant in Kind since unit tests
  (`TestNewGRPCPush_AgentErrorSurfaces`) and the agent's own existing
  `push_desired_checksum_mismatch_...` test already cover that path, and
  the task explicitly treats the Kind demo as corroborating evidence, not
  the only bar.
- No TLS/mTLS added, `NewLocalPush` not deleted, `agent/`/`contract/`/
  `internal/render`/`internal/ddi` not modified — all per explicit
  constraints.
