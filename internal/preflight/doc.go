// Package preflight gates init and boot before any on-disk side effect
// runs. The intent: fail BEFORE kubeadm.Ensure / backup.Create / state
// writes touch disk, so a transient disk-full or permission error never
// causes greenboot to roll back a perfectly working cluster.
//
//	preflight.go   shared gate (writability probes + free-space check)
//	               and boot-only scratch allocation (AllocateWorkspace).
//	               Boot calls Preflight then AllocateWorkspace.
//	preinstall.go  install-time gate. Used by `nanokube init`; delegates
//	               to Preflight today plus any init-only checks added
//	               in the future.
//
// Both `nanokube init` and `nanokube boot` import this package; runtime
// health (apiserver, cluster) lives in package healthcheck instead.
package preflight
