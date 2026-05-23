//go:build e2e

// Package e2e is nanokube's end-to-end suite. It drives the full
// bootstrap → boot → workload → reset lifecycle on a single Ubuntu
// host that already ships kubelet, CRI-O, kubectl, and crictl (see
// test/e2e/setup.sh for provisioning).
//
// The suite is gated by //go:build e2e — `go test ./...` does not see
// it, only `go test -tags e2e ./test/e2e/...` does. It must run as
// root (the binary mutates /etc/kubernetes, /var/lib/etcd, …) and
// will refuse to start otherwise.
//
// Method ordering is load-bearing. Tests are named TestNN_Group_Case
// and run alphabetically by reflect.Type.Method order (testify's
// dispatch rule), which gives us deterministic suite execution
// without manually wiring t.Run subtests. Do not renumber casually;
// later tests rely on cluster state established by earlier ones (a
// Test07 boot test, for instance, assumes Test04 init has run).
//
// Env vars:
//
//	NANOKUBE_E2E_KEEP=1         keep /tmp/nanokube-e2e-<pid> after the
//	                            suite (default: kept only on failure)
//	NANOKUBE_E2E_SKIP_SETUP=1   skip bash setup.sh in SetupSuite (use
//	                            when iterating against an already
//	                            provisioned host)
//	NANOKUBE_E2E_PREBUILT=1     skip `go build` + install in SetupSuite
//	                            (CI sets this because it builds in a
//	                            prior step)
package e2e
