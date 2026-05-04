// Package testutil holds helpers shared across nanokube *_test.go files.
// Production code must not import this package: doing so pulls the
// testing runtime into the binary.
package testutil

import (
	"path/filepath"
	"testing"

	"github.com/MatchaScript/nanokube/internal/paths"
)

// UseTempPaths rewrites every variable in the paths package to live under
// a freshly created t.TempDir() root, and registers a Cleanup that
// restores the originals. Returns the root so callers can seed files
// directly under it.
//
// Because paths.* are process-global package variables, tests using this
// helper must not run in parallel with each other. Go test ordering is
// already serial within a single package; avoid t.Parallel() in tests
// that call this.
func UseTempPaths(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	orig := snapshot()
	t.Cleanup(func() { restore(orig) })

	paths.ConfigDir = filepath.Join(root, "etc/nanokube")
	paths.ConfigFile = filepath.Join(paths.ConfigDir, "config.yaml")
	paths.OperatorCertsDir = filepath.Join(paths.ConfigDir, "certs")
	paths.KubernetesDir = filepath.Join(root, "etc/kubernetes")
	paths.PKIDir = filepath.Join(paths.KubernetesDir, "pki")
	paths.EtcdPKIDir = filepath.Join(paths.PKIDir, "etcd")
	paths.ManifestsDir = filepath.Join(paths.KubernetesDir, "manifests")
	paths.KubeAPIServerManifest = filepath.Join(paths.ManifestsDir, "kube-apiserver.yaml")
	paths.KubeconfigDir = paths.KubernetesDir
	paths.AdminKubeconfig = filepath.Join(paths.KubeconfigDir, "admin.conf")
	paths.SuperAdminKubeconfig = filepath.Join(paths.KubeconfigDir, "super-admin.conf")
	paths.KubeletKubeconfig = filepath.Join(paths.KubeconfigDir, "kubelet.conf")
	paths.CMKubeconfig = filepath.Join(paths.KubeconfigDir, "controller-manager.conf")
	paths.SchedKubeconfig = filepath.Join(paths.KubeconfigDir, "scheduler.conf")
	paths.KubeletDir = filepath.Join(root, "var/lib/kubelet")
	paths.KubeletConfigFile = filepath.Join(paths.KubeletDir, "config.yaml")
	paths.KubeletFlagsEnvFile = filepath.Join(paths.KubeletDir, "kubeadm-flags.env")
	paths.EtcdDataDir = filepath.Join(root, "var/lib/etcd")
	paths.NanoKubeVarDir = filepath.Join(root, "var/lib/nanokube")
	paths.StateDir = filepath.Join(paths.NanoKubeVarDir, "state")
	paths.BackupsDir = filepath.Join(paths.NanoKubeVarDir, "backups")
	paths.LastBootFile = filepath.Join(paths.StateDir, "last-boot.json")
	paths.LastEventFile = filepath.Join(paths.StateDir, "last-event")
	paths.RestoreMarker = filepath.Join(paths.BackupsDir, "restore")

	return root
}

type pathsSnapshot struct {
	ConfigDir, ConfigFile                              string
	OperatorCertsDir                                   string
	KubernetesDir, PKIDir, EtcdPKIDir, ManifestsDir    string
	KubeAPIServerManifest                              string
	KubeconfigDir                                      string
	AdminKubeconfig, KubeletKubeconfig                 string
	SuperAdminKubeconfig                               string
	CMKubeconfig, SchedKubeconfig                      string
	KubeletDir, KubeletConfigFile, KubeletFlagsEnvFile string
	EtcdDataDir                                        string
	NanoKubeVarDir, StateDir, BackupsDir                string
	LastBootFile, LastEventFile, RestoreMarker         string
}

func snapshot() pathsSnapshot {
	return pathsSnapshot{
		paths.ConfigDir, paths.ConfigFile,
		paths.OperatorCertsDir,
		paths.KubernetesDir, paths.PKIDir, paths.EtcdPKIDir, paths.ManifestsDir,
		paths.KubeAPIServerManifest,
		paths.KubeconfigDir,
		paths.AdminKubeconfig, paths.KubeletKubeconfig,
		paths.SuperAdminKubeconfig,
		paths.CMKubeconfig, paths.SchedKubeconfig,
		paths.KubeletDir, paths.KubeletConfigFile, paths.KubeletFlagsEnvFile,
		paths.EtcdDataDir,
		paths.NanoKubeVarDir, paths.StateDir, paths.BackupsDir,
		paths.LastBootFile, paths.LastEventFile, paths.RestoreMarker,
	}
}

func restore(s pathsSnapshot) {
	paths.ConfigDir = s.ConfigDir
	paths.ConfigFile = s.ConfigFile
	paths.OperatorCertsDir = s.OperatorCertsDir
	paths.KubernetesDir = s.KubernetesDir
	paths.PKIDir = s.PKIDir
	paths.EtcdPKIDir = s.EtcdPKIDir
	paths.ManifestsDir = s.ManifestsDir
	paths.KubeAPIServerManifest = s.KubeAPIServerManifest
	paths.KubeconfigDir = s.KubeconfigDir
	paths.AdminKubeconfig = s.AdminKubeconfig
	paths.KubeletKubeconfig = s.KubeletKubeconfig
	paths.SuperAdminKubeconfig = s.SuperAdminKubeconfig
	paths.CMKubeconfig = s.CMKubeconfig
	paths.SchedKubeconfig = s.SchedKubeconfig
	paths.KubeletDir = s.KubeletDir
	paths.KubeletConfigFile = s.KubeletConfigFile
	paths.KubeletFlagsEnvFile = s.KubeletFlagsEnvFile
	paths.EtcdDataDir = s.EtcdDataDir
	paths.NanoKubeVarDir = s.NanoKubeVarDir
	paths.StateDir = s.StateDir
	paths.BackupsDir = s.BackupsDir
	paths.LastBootFile = s.LastBootFile
	paths.LastEventFile = s.LastEventFile
	paths.RestoreMarker = s.RestoreMarker
}
