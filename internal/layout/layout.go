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

import "path/filepath"

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
	CredentialsDir string // /var/lib/nanokube/credentials

	// Kubernetes (KubernetesDir is also the kubeconfig directory)
	KubernetesDir              string // /etc/kubernetes
	PKIDir                     string // /etc/kubernetes/pki
	EtcdPKIDir                 string // /etc/kubernetes/pki/etcd
	ManifestsDir               string // /etc/kubernetes/manifests
	KubeAPIServerManifest      string
	AdminKubeconfig            string
	KubeletKubeconfig          string
	BootstrapKubeletKubeconfig string // /etc/kubernetes/bootstrap-kubelet.conf
	CMKubeconfig               string
	SchedKubeconfig            string
	SuperAdminKubeconfig       string

	// Kubelet
	KubeletDir          string // /var/lib/kubelet
	KubeletConfigFile   string
	KubeletFlagsEnvFile string

	// etcd
	EtcdDataDir string // /var/lib/etcd
}

// Default returns the production layout — the canonical kubeadm +
// nanokube on-disk locations.
func Default() Layout {
	return Rooted("/")
}

// Rooted returns the canonical kubeadm + nanokube layout with every path
// rooted under root instead of "/". Default() is Rooted("/");
// internal/layouttest.New(t) is Rooted(t.TempDir()).
func Rooted(root string) Layout {
	configDir := filepath.Join(root, "etc/nanokube")
	nkVarDir := filepath.Join(root, "var/lib/nanokube")
	stateDir := filepath.Join(nkVarDir, "state")
	backups := filepath.Join(nkVarDir, "backups")

	kdir := filepath.Join(root, "etc/kubernetes")
	pki := filepath.Join(kdir, "pki")
	etcdPKI := filepath.Join(pki, "etcd")
	mfs := filepath.Join(kdir, "manifests")

	kubelet := filepath.Join(root, "var/lib/kubelet")

	return Layout{
		ConfigDir:      configDir,
		ConfigFile:     filepath.Join(configDir, "config.yaml"),
		NanoKubeVarDir: nkVarDir,
		StateDir:       stateDir,
		LastBootFile:   filepath.Join(stateDir, "last-boot.json"),
		LastEventFile:  filepath.Join(stateDir, "last-event"),
		BackupsDir:     backups,
		RestoreMarker:  filepath.Join(backups, "restore"),
		CredentialsDir: filepath.Join(nkVarDir, "credentials"),

		KubernetesDir:              kdir,
		PKIDir:                     pki,
		EtcdPKIDir:                 etcdPKI,
		ManifestsDir:               mfs,
		KubeAPIServerManifest:      filepath.Join(mfs, "kube-apiserver.yaml"),
		AdminKubeconfig:            filepath.Join(kdir, "admin.conf"),
		KubeletKubeconfig:          filepath.Join(kdir, "kubelet.conf"),
		BootstrapKubeletKubeconfig: filepath.Join(kdir, "bootstrap-kubelet.conf"),
		CMKubeconfig:               filepath.Join(kdir, "controller-manager.conf"),
		SchedKubeconfig:            filepath.Join(kdir, "scheduler.conf"),
		SuperAdminKubeconfig:       filepath.Join(kdir, "super-admin.conf"),

		KubeletDir:          kubelet,
		KubeletConfigFile:   filepath.Join(kubelet, "config.yaml"),
		KubeletFlagsEnvFile: filepath.Join(kubelet, "kubeadm-flags.env"),

		EtcdDataDir: filepath.Join(root, "var/lib/etcd"),
	}
}
