// Package preflight gates init and boot before any on-disk side effect
// runs. The intent: fail BEFORE kubeadm.Ensure / backup.Create / state
// writes touch disk, so a transient disk-full or permission error never
// causes greenboot to roll back a perfectly working cluster.
//
//	preinstall.go  install-time gate (writability probes + free-space
//	               check). Used by `nanokube init`.
//
// Both `nanokube init` and `nanokube boot` import this package; runtime
// health (apiserver, cluster) lives in package healthcheck instead.
package preflight
