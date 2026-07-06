// Package fixtures holds golden fixtures for the desired-document
// cross-language contract: a <name>.raw confext DDI blob (built by
// internal/ddi) and its sidecar <name>.json (a protojson-marshaled
// desiredpb.DesiredMetadata), for every canonical render.Desired this
// repo commits as a fixture. fixtures_test.go checks each pair for
// internal consistency without needing systemd-repart; the agent's
// Rust-side tests (Step 1 実装項目4) read the same files directly.
//
// Regenerate (or add a fixture) with contract/fixtures/gen. See that
// package's doc comment for why it's split into a "render" step (run
// natively, on the same kind of host that will later run `go test`)
// and a "build" step (needs systemd-repart + mkfs.erofs on PATH — a
// Fedora container with systemd-container, systemd-udev, and
// erofs-utils installed is enough; building the DDI itself is
// unprivileged). From the repo root:
//
//	mise exec -- go run ./contract/fixtures/gen render <manifest-dir>
//	# then, inside the container, with the repo and <manifest-dir>
//	# both bind-mounted at the same paths and the container's workdir
//	# set to the repo root:
//	<gen-binary> build <manifest-dir>
package fixtures

//go:generate go run ./gen render /tmp/nanokube-fixture-manifest
