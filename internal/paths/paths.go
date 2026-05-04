// Package paths centralises every filesystem location nanokube reads or writes.
// Having one source of truth here makes it straightforward to relocate trees
// for tests (override these vars before calling into other packages).
//
// Declared as `var` rather than `const` specifically so tests can repoint
// them at t.TempDir() via a helper (see the internal/paths test helper).
// Production code must not mutate them.
package paths

var (
	// ConfigDir holds user-facing nanokube configuration.
	ConfigDir  = "/etc/nanokube"
	ConfigFile = ConfigDir + "/config.yaml"

	// OperatorCertsDir is the optional BYOCA seed directory. Operators
	// may place CA crt+key pairs here (cluster CA = ca.{crt,key},
	// etcd CA = etcd/ca.{crt,key}, front-proxy CA = front-proxy-ca.{crt,key}).
	// `nanokube init` copies what exists into PKIDir; absent files mean
	// nanokube self-signs. Survives `nanokube reset` (Ownership model:
	// operator source vs. build artifact).
	OperatorCertsDir = ConfigDir + "/certs"

	// KubernetesDir is the standard kubelet-facing tree.
	KubernetesDir         = "/etc/kubernetes"
	PKIDir                = KubernetesDir + "/pki"
	EtcdPKIDir            = PKIDir + "/etcd"
	ManifestsDir          = KubernetesDir + "/manifests"
	KubeAPIServerManifest = ManifestsDir + "/kube-apiserver.yaml"
	KubeconfigDir         = KubernetesDir
	AdminKubeconfig       = KubeconfigDir + "/admin.conf"
	KubeletKubeconfig     = KubeconfigDir + "/kubelet.conf"
	CMKubeconfig          = KubeconfigDir + "/controller-manager.conf"
	SchedKubeconfig       = KubeconfigDir + "/scheduler.conf"
	// SuperAdminKubeconfig is the system:masters-bound break-glass cred.
	// nanokube creates it during `init`, uses it once to seed the
	// kubeadm:cluster-admins ClusterRoleBinding, then deletes it. Operators
	// who need to re-issue it run `nanokube kubeconfig super-admin`.
	SuperAdminKubeconfig = KubeconfigDir + "/super-admin.conf"

	// KubeletDir is kubelet's own state directory.
	KubeletDir          = "/var/lib/kubelet"
	KubeletConfigFile   = KubeletDir + "/config.yaml"
	KubeletFlagsEnvFile = KubeletDir + "/kubeadm-flags.env"

	// EtcdDataDir is the etcd static pod's data directory. Snapshotted by
	// nanokube on every boot before kubelet brings etcd back up.
	EtcdDataDir = "/var/lib/etcd"

	// NanoKubeVarDir holds all nanokube-owned mutable state (state files,
	// backups). /var survives bootc rollback; we explicitly version
	// sub-trees ourselves so rollback + restore is decoupled from /var.
	NanoKubeVarDir = "/var/lib/nanokube"
	StateDir      = NanoKubeVarDir + "/state"
	BackupsDir    = NanoKubeVarDir + "/backups"

	// State files (under StateDir).
	LastBootFile  = StateDir + "/last-boot.json"
	LastEventFile = StateDir + "/last-event"

	// RestoreMarker is touched by the greenboot red.d hook on a rollback
	// boot to request that the next boot restore a backup for the
	// (post-rollback) deployment.
	RestoreMarker = BackupsDir + "/restore"
)
