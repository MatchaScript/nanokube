// Package layout enumerates every filesystem location nanokube reads or
// writes. A single value is built in cmd/nanokube/root.go via Default()
// and passed down to every component, replacing the global var-based
// internal/paths package.
//
// Layout is a value type. Components extract the fields they need at
// the call site; the package exposes no subsystem accessors so the
// "certsLayout" / "kubeadm.DefaultLayout" duplication never returns.
//
// Tests build Layout via internal/layouttest.New(t), which roots every
// path under t.TempDir() with no global state — safe under t.Parallel().
package layout

// Layout holds every filesystem path nanokube reads or writes. Production
// callers receive Default(); tests receive layouttest.New(t).
type Layout struct {
	// nanokube own config and state
	ConfigDir      string // /etc/nanokube
	ConfigFile     string // /etc/nanokube/config.yaml
	NanoKubeVarDir string // /var/lib/nanokube
	StateDir       string // /var/lib/nanokube/state
	LastBootFile   string
	LastEventFile  string
	BackupsDir     string // /var/lib/nanokube/backups
	RestoreMarker  string

	// Kubernetes (KubernetesDir is also the kubeconfig directory)
	KubernetesDir         string // /etc/kubernetes
	PKIDir                string // /etc/kubernetes/pki
	EtcdPKIDir            string // /etc/kubernetes/pki/etcd
	ManifestsDir          string // /etc/kubernetes/manifests
	KubeAPIServerManifest string
	AdminKubeconfig       string
	KubeletKubeconfig     string
	CMKubeconfig          string
	SchedKubeconfig       string
	SuperAdminKubeconfig  string

	// Kubelet
	KubeletDir          string // /var/lib/kubelet
	KubeletConfigFile   string
	KubeletFlagsEnvFile string

	// etcd
	EtcdDataDir string // /var/lib/etcd
}

// Default returns the production layout. Values match the legacy
// internal/paths package 1:1; the layout_test.go parity test enforces this.
func Default() Layout {
	const (
		configDir = "/etc/nanokube"
		nkVarDir  = "/var/lib/nanokube"
		stateDir  = nkVarDir + "/state"
		backups   = nkVarDir + "/backups"

		kdir = "/etc/kubernetes"
		pki  = kdir + "/pki"
		etcd = pki + "/etcd"
		mfs  = kdir + "/manifests"

		kubelet = "/var/lib/kubelet"
	)
	return Layout{
		ConfigDir:      configDir,
		ConfigFile:     configDir + "/config.yaml",
		NanoKubeVarDir: nkVarDir,
		StateDir:       stateDir,
		LastBootFile:   stateDir + "/last-boot.json",
		LastEventFile:  stateDir + "/last-event",
		BackupsDir:     backups,
		RestoreMarker:  backups + "/restore",

		KubernetesDir:         kdir,
		PKIDir:                pki,
		EtcdPKIDir:            etcd,
		ManifestsDir:          mfs,
		KubeAPIServerManifest: mfs + "/kube-apiserver.yaml",
		AdminKubeconfig:       kdir + "/admin.conf",
		KubeletKubeconfig:     kdir + "/kubelet.conf",
		CMKubeconfig:          kdir + "/controller-manager.conf",
		SchedKubeconfig:       kdir + "/scheduler.conf",
		SuperAdminKubeconfig:  kdir + "/super-admin.conf",

		KubeletDir:          kubelet,
		KubeletConfigFile:   kubelet + "/config.yaml",
		KubeletFlagsEnvFile: kubelet + "/kubeadm-flags.env",

		EtcdDataDir: "/var/lib/etcd",
	}
}
