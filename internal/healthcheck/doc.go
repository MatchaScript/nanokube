// Package healthcheck answers three layers of "is this nanokube healthy?":
//
//	preflight.go   host-side: writability probes + free-space gate, run
//	               at the start of init/boot to fail before kubeadm
//	               phases or backups touch disk.
//	apiserver.go   apiserver endpoint: /readyz probe used to wait out
//	               kube-apiserver's static-pod startup window.
//	cluster.go     cluster resources: Node Ready + the three control-
//	               plane static pods Ready, which `nanokube healthcheck`
//	               + lifecycle.Boot + initialize.Run all gate on.
//
// Splitting by layer keeps each call site importing what it actually
// needs and keeps the names describing the concern (apiserver, cluster,
// preflight) rather than the mechanism (client, kubeconfig, syscall).
package healthcheck
