package layout

import "testing"

// TestDefault_MatchesCanonicalPaths pins Default()'s output against the
// literal path strings it hardcoded before the Rooted(root) refactor.
// Rooted builds every field via filepath.Join instead of "+"
// concatenation; nothing else in the tree regression-tests that the
// rewrite didn't drift a field, so a future edit to Rooted could
// silently change where nanokube reads or writes on a real host with
// no test catching it.
func TestDefault_MatchesCanonicalPaths(t *testing.T) {
	want := Layout{
		ConfigDir:                  "/etc/nanokube",
		ConfigFile:                 "/etc/nanokube/config.yaml",
		NanoKubeVarDir:             "/var/lib/nanokube",
		StateDir:                   "/var/lib/nanokube/state",
		LastBootFile:               "/var/lib/nanokube/state/last-boot.json",
		LastEventFile:              "/var/lib/nanokube/state/last-event",
		BackupsDir:                 "/var/lib/nanokube/backups",
		RestoreMarker:              "/var/lib/nanokube/backups/restore",
		CredentialsDir:             "/var/lib/nanokube/credentials",
		KubernetesDir:              "/etc/kubernetes",
		PKIDir:                     "/etc/kubernetes/pki",
		EtcdPKIDir:                 "/etc/kubernetes/pki/etcd",
		ManifestsDir:               "/etc/kubernetes/manifests",
		KubeAPIServerManifest:      "/etc/kubernetes/manifests/kube-apiserver.yaml",
		AdminKubeconfig:            "/etc/kubernetes/admin.conf",
		KubeletKubeconfig:          "/etc/kubernetes/kubelet.conf",
		BootstrapKubeletKubeconfig: "/etc/kubernetes/bootstrap-kubelet.conf",
		CMKubeconfig:               "/etc/kubernetes/controller-manager.conf",
		SchedKubeconfig:            "/etc/kubernetes/scheduler.conf",
		SuperAdminKubeconfig:       "/etc/kubernetes/super-admin.conf",
		KubeletDir:                 "/var/lib/kubelet",
		KubeletConfigFile:          "/var/lib/kubelet/config.yaml",
		KubeletFlagsEnvFile:        "/var/lib/kubelet/kubeadm-flags.env",
		EtcdDataDir:                "/var/lib/etcd",
	}
	got := Default()
	if got != want {
		t.Errorf("Default() = %+v\nwant %+v", got, want)
	}
}

// TestRooted_JoinsUnderRoot confirms Rooted mirrors Default()'s
// relative structure under an arbitrary root, the property
// internal/layouttest.New(t) and internal/render.Credentials both rely
// on.
func TestRooted_JoinsUnderRoot(t *testing.T) {
	got := Rooted("/tmp/example")
	want := Layout{
		ConfigDir:                  "/tmp/example/etc/nanokube",
		ConfigFile:                 "/tmp/example/etc/nanokube/config.yaml",
		NanoKubeVarDir:             "/tmp/example/var/lib/nanokube",
		StateDir:                   "/tmp/example/var/lib/nanokube/state",
		LastBootFile:               "/tmp/example/var/lib/nanokube/state/last-boot.json",
		LastEventFile:              "/tmp/example/var/lib/nanokube/state/last-event",
		BackupsDir:                 "/tmp/example/var/lib/nanokube/backups",
		RestoreMarker:              "/tmp/example/var/lib/nanokube/backups/restore",
		CredentialsDir:             "/tmp/example/var/lib/nanokube/credentials",
		KubernetesDir:              "/tmp/example/etc/kubernetes",
		PKIDir:                     "/tmp/example/etc/kubernetes/pki",
		EtcdPKIDir:                 "/tmp/example/etc/kubernetes/pki/etcd",
		ManifestsDir:               "/tmp/example/etc/kubernetes/manifests",
		KubeAPIServerManifest:      "/tmp/example/etc/kubernetes/manifests/kube-apiserver.yaml",
		AdminKubeconfig:            "/tmp/example/etc/kubernetes/admin.conf",
		KubeletKubeconfig:          "/tmp/example/etc/kubernetes/kubelet.conf",
		BootstrapKubeletKubeconfig: "/tmp/example/etc/kubernetes/bootstrap-kubelet.conf",
		CMKubeconfig:               "/tmp/example/etc/kubernetes/controller-manager.conf",
		SchedKubeconfig:            "/tmp/example/etc/kubernetes/scheduler.conf",
		SuperAdminKubeconfig:       "/tmp/example/etc/kubernetes/super-admin.conf",
		KubeletDir:                 "/tmp/example/var/lib/kubelet",
		KubeletConfigFile:          "/tmp/example/var/lib/kubelet/config.yaml",
		KubeletFlagsEnvFile:        "/tmp/example/var/lib/kubelet/kubeadm-flags.env",
		EtcdDataDir:                "/tmp/example/var/lib/etcd",
	}
	if got != want {
		t.Errorf("Rooted(/tmp/example) = %+v\nwant %+v", got, want)
	}
}
