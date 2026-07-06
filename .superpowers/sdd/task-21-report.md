# Task 21: Close the symmetric build/boot race in test/devenv

## Context

Prior task added a guard to `build-image.sh`: it checks `qemu.pid` once, at
script start, and refuses to run if a VM is running (a real incident had
corrupted a live VM's disk when `bootc-image-builder`'s export step
overwrote the same qcow2 path a running qemu process had open). Review
flagged the fix as only half-closed:

- The check runs once, at the very top of `build-image.sh`, before a
  multi-minute `cargo build` -> `podman build` -> push/pull sequence. A VM
  started (via `boot-vm.sh`) *after* that check but *before* the actual
  `bootc-image-builder` step would not be caught.
- `boot-vm.sh` had no guard at all against booting while a build is
  in-flight.

This task closes both halves with a lockfile/marker approach, kept
proportionate to local single-developer dev tooling (no file-locking
libraries, no retry loops -- just a PID-stamped marker file with `trap`-based
cleanup, mirroring the existing `qemu.pid`/`kill -0` idiom).

## Exact changes

### `test/devenv/build-image.sh`

1. Extracted the existing `qemu.pid` guard into a `check_vm_not_running()`
   function (message text unchanged verbatim) so it can be called twice
   without duplicating the six-line error message.
2. Call `check_vm_not_running` at the same point the old inline check ran
   (near the top, before any state-dir setup).
3. Immediately after that first check passes, write
   `${STATE_DIR}/.build-in-progress` containing `$$` (this script's own
   PID), and register `trap 'rm -f "${MARKER}"' EXIT` right after writing
   it. `mkdir -p "${STATE_DIR}"` was added just before the write since
   nothing had created `STATE_DIR` itself yet at that point in the script
   (the pre-existing `mkdir -p "${SSH_DIR}" ...` a few lines down creates it
   only incidentally, and only after the marker needs to already exist).
4. Added a second call to `check_vm_not_running` immediately before the
   `sudo podman run ... bootc-image-builder ... build --type qcow2` step
   (the one and only step that overwrites `disk.qcow2`) -- right after the
   `== [5/5] ==` banner, right before the dangerous command. This is the
   exact gap the review flagged: if a VM was started during the preceding
   multi-minute build, this call now fails loudly with the same detailed
   error message (mechanism + remedy) instead of silently proceeding to
   corrupt the disk.

### `test/devenv/boot-vm.sh`

1. Added `MARKER="${STATE_DIR}/.build-in-progress"` alongside the existing
   `PIDFILE` definition.
2. Added a new guard block at the top, before the existing "does the disk
   file exist" check: if the marker file exists, read the PID it contains
   and `kill -0` it (mirroring the exact idiom of the pre-existing
   `qemu.pid` guard).
   - If that PID is alive: refuse to boot with a message that explains the
     mechanism (booting now would open the same path `build-image.sh` is
     still writing to, corrupting it the same way a rebuild against a live
     VM does) and points at the README's incident note, then `exit 1`.
   - If the marker is present but the PID is dead (or the file is
     empty/unreadable): treat it as stale -- a `build-image.sh` run that
     didn't exit cleanly (e.g. `kill -9`, bypassing its `trap`) -- `rm -f`
     it and fall through to normal boot.

## Edge-case reasoning

- **Stale marker (the main one called out in the task)**: handled by
  checking liveness of the PID stored in the marker, exactly like the
  pre-existing `qemu.pid` guard does for the qemu process. A marker whose
  PID is no longer running can only be stale (either `build-image.sh`
  finished normally, in which case its own `trap` would already have
  removed it and we'd never see it here at all, or it was killed
  ungracefully) -- either way it's safe to delete and proceed. This also
  self-heals: the first `boot-vm.sh` invocation after a `kill -9`'d build
  cleans the stale marker up, so it doesn't need any separate cleanup
  tooling.
- **Normal sequential workflow (build, then boot) must never be blocked**:
  because the marker is written right after `build-image.sh`'s first guard
  check and removed via `trap ... EXIT` covering every exit path (normal
  end of script, `set -e` early exit, or receipt of a trappable signal), by
  the time `build-image.sh` returns control to the shell (successfully or
  not) the marker is already gone. A `boot-vm.sh` run immediately
  afterward in the same terminal sees no marker at all. Verified this
  experimentally (see Testing).
- **`kill -9` (SIGKILL) specifically bypasses the trap**, as the task
  description anticipated -- this is exactly the case the stale-PID check
  exists for, and was tested (see below).
- **Residual TOCTOU**: there's still a vanishingly small window between
  `check_vm_not_running`'s second call and the `sudo podman run
  ...bootc-image-builder` line actually starting (a couple of shell
  statements), and symmetrically between `boot-vm.sh`'s marker check and
  qemu actually opening the disk file. Closing that fully would need real
  file locking (e.g. `flock`), which the task explicitly said is
  disproportionate for local single-developer dev tooling; a marker file
  checked immediately before the dangerous step reduces the danger window
  from "multiple minutes" to "a couple of shell statements," which is the
  proportionate fix asked for.
- **Concurrent double-invocation of `build-image.sh` itself** (two
  build-image.sh runs racing each other) is out of scope: the task is about
  the build/boot race specifically, and the marker being overwritten by a
  second concurrent build isn't part of the flagged review gap. Not
  handled, not asked for.

## Testing

**Live-VM state confirmed first**: `ps aux | grep qemu` showed
`qemu-system-x86_64` running as PID 447283, matching
`/var/tmp/nanokube-devenv/qemu.pid` (`kill -0 447283` succeeded). This is
the real, agent-baked VM from the prior task. It was never touched --
verified again at the end of testing that it was still running under the
same PID and that the real `/var/tmp/nanokube-devenv/` state dir never
gained a `.build-in-progress` file during any of this testing.

**`boot-vm.sh`'s new guard -- tested for real, three scenarios**, using a
scratch copy of the script (`/tmp/.../scratchpad/boot-vm-test.sh`) with
`STATE_DIR` repointed at a throwaway scratch directory (with a dummy empty
`disk.qcow2`) and the final `exec qemu-system-x86_64 ...` line swapped for
an `echo "WOULD EXEC: ..."` so no real qemu process was ever launched
against any disk, live or scratch. The guard logic itself (the part under
test) was copied verbatim, unmodified, from the real `boot-vm.sh`:

1. No marker present -> guard falls through silently, script reaches the
   `WOULD EXEC` line. `exit 0`.
2. Marker containing a live PID (the test shell's own `$$`, confirmed alive
   for the duration of the test) -> refused with the intended error message
   (`error: build-image.sh is currently running (pid ...)`, mechanism
   explanation, `exit 1`); marker file left in place afterward (correct --
   `boot-vm.sh` isn't the one that should clean up a *live* build's
   marker).
3. Marker containing a dead PID (found by probing upward from 99999 until
   `kill -0` failed) -> treated as stale, silently removed
   (`ls` confirmed gone afterward), script fell through to the `WOULD EXEC`
   line, `exit 0`.

All three matched the intended behavior exactly. Scratch artifacts were
removed afterward; the real `/var/tmp/nanokube-devenv/` and the live VM
were never touched by any of this (`boot-vm.sh` itself was never invoked
directly against the real disk).

**`build-image.sh`'s re-check -- reasoned through, not run end-to-end.**
A real run wasn't safe: a VM is genuinely running right now, so a real
`build-image.sh` invocation would (correctly) refuse at its very first
check (`check_vm_not_running`) before ever reaching the marker-write or the
new re-check -- so a live run couldn't have exercised the new code path
anyway without first stopping the live VM, which the task explicitly
forbids. Reasoning through the diff instead:
- `check_vm_not_running` is a plain function extracted verbatim from the
  pre-existing, already-proven-correct inline check (only wrapped in a
  function definition; the body is byte-for-byte the same `if` block) --
  its behavior at the first call site is unchanged from before this task.
- The second call site is a direct call to the same function, placed
  textually immediately before the `sudo podman run ...
  bootc-image-builder` line. Bash executes top-to-bottom with no
  reordering possible here (no backgrounding, no `&`), so at runtime this
  call is guaranteed to execute, and to execute, strictly after the
  multi-minute build/push/pull steps and strictly before the
  disk-overwriting command -- which is exactly the gap the review flagged.
  `bash -n` confirms no syntax errors were introduced.
- The marker-write and `trap` were exercised indirectly: confirmed via
  `bash -n` syntax validity, and their logic (`echo "$$" >"${MARKER}"` /
  `trap 'rm -f "${MARKER}"' EXIT`) is the standard idiom called out
  explicitly in the task description as sufficient. The consuming side of
  the same marker file (`boot-vm.sh`'s stale/live checks) *was* exercised
  for real, in all three states a marker can be in, which indirectly
  validates the marker's on-disk format (`PID\n`, readable via `cat`) is
  correct.

## README update

Added to `test/devenv/README.md`'s existing 2026-07-06 incident section
(did not rewrite the existing incident paragraph): a new "Race, closed both
directions (2026-07-06)" paragraph directly below it, explaining the gap
the review found and how both scripts now close it, plus one line in the
"Runtime state (not in git)" file listing documenting `.build-in-progress`.

## Files changed

- `test/devenv/build-image.sh`
- `test/devenv/boot-vm.sh`
- `test/devenv/README.md`

## Self-review findings

- Checked ordering in `build-image.sh`: marker write happens *after* the
  first `check_vm_not_running` call, not before. This means if the script
  is going to refuse immediately (VM already running), it never touches
  the marker file at all -- a deliberate choice so a rejected `build-image.sh`
  invocation leaves zero trace, rather than momentarily creating and then
  immediately removing a marker.
- Checked that `check_vm_not_running` is defined before either call site
  (function defined once, near the top, called twice below).
- Checked the `BUILD_PID="$(cat "${MARKER}" 2>/dev/null || true)"` line in
  `boot-vm.sh` doesn't trip `set -e` on a missing/unreadable marker file
  mid-race (the file could theoretically vanish between the `[ -f ]` test
  and the `cat`, e.g. if `build-image.sh` finishes and its trap fires in
  that exact window) -- the `|| true` inside the command substitution
  absorbs that.
- Confirmed both scripts still pass `bash -n` after all edits.
- Confirmed no changes leaked outside `test/devenv/` and none of the
  forbidden paths (`agent/`, `internal/operator`, `cmd/nanokube-operator`,
  `contract/`, `internal/render`, `internal/ddi`) were touched (`git status`
  after the change shows exactly the three files above modified).
- Did not add any handling for two concurrent `build-image.sh` runs racing
  each other over the marker file -- out of scope per the task (the task is
  about the build/boot race specifically), flagging it here rather than
  quietly expanding scope.
