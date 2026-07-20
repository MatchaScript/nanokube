//go:build kubeadm_parity

package render

// TestControlPlaneManifests_Parity is the development-time reference
// check IMPLEMENTATION_PLAN.md §1.3 calls for ("テンプレート初版の正しさは
// kubeadm の生成物との突き合わせで確立する（開発時の参照値。恒久の CI 依存
// にはしない）"): it drives kubeadm's own phase functions directly and
// compares their output to ControlPlaneManifests's, for the same
// manifestVariants matrix golden_test.go pins. Gated behind the
// kubeadm_parity build tag so it never runs under plain `go test` — it
// imports cmd/kubeadm/app/phases/controlplane and .../phases/etcd, which
// production code (render.go, manifests.go) no longer does (§1.2/§11).
//
// A raw byte comparison isn't meaningful here: kubeadm's live call adds
// whichever of ambientCACertMountPaths happen to exist on THIS host,
// which our construction deliberately never emits (manifests.go's doc
// comment, deviation source: an ambient os.Stat kubeadm performs at
// render time). That divergence is stripped from kubeadm's side before
// comparing; everything else — commands, images, every other volume and
// mount, env, probes, resources, annotations, labels — is required to
// match exactly.
import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/controlplane"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/etcd"
	kubeadmutil "k8s.io/kubernetes/cmd/kubeadm/app/util"
	staticpodutil "k8s.io/kubernetes/cmd/kubeadm/app/util/staticpod"
)

// parityAmbientVolumeNames is the volume-name form of
// ambientCACertMountPaths (controlplane/volumes.go's
// strings.Replace(path, "/", "-", -1)[1:] mangling), used to strip the
// one known, host-dependent divergence from kubeadm's live output
// before comparing.
var parityAmbientVolumeNames = func() map[string]bool {
	m := make(map[string]bool, len(ambientCACertMountPaths))
	for _, p := range ambientCACertMountPaths {
		m[strings.Replace(p, "/", "-", -1)[1:]] = true
	}
	return m
}()

func stripAmbientForParity(pod *corev1.Pod) {
	var vols []corev1.Volume
	for _, v := range pod.Spec.Volumes {
		if parityAmbientVolumeNames[v.Name] {
			continue
		}
		vols = append(vols, v)
	}
	pod.Spec.Volumes = vols

	for i := range pod.Spec.Containers {
		var mounts []corev1.VolumeMount
		for _, m := range pod.Spec.Containers[i].VolumeMounts {
			if parityAmbientVolumeNames[m.Name] {
				continue
			}
			mounts = append(mounts, m)
		}
		pod.Spec.Containers[i].VolumeMounts = mounts
	}
}

func TestControlPlaneManifests_Parity(t *testing.T) {
	for _, v := range manifestVariants() {
		t.Run(v.name, func(t *testing.T) {
			cfg := variantConfig(t, v)

			ours, err := ControlPlaneManifests(cfg)
			if err != nil {
				t.Fatalf("ControlPlaneManifests: %v", err)
			}

			scratch := t.TempDir()
			own := *cfg
			own.ClusterConfiguration.CertificatesDir = nodePKIDir
			if err := etcd.CreateLocalEtcdStaticPodManifestFile(
				scratch, "", own.NodeRegistration.Name, &own.ClusterConfiguration, &own.LocalAPIEndpoint, false,
			); err != nil {
				t.Fatalf("kubeadm etcd manifest: %v", err)
			}
			specs := controlplane.GetStaticPodSpecs(&own.ClusterConfiguration, &own.LocalAPIEndpoint, []kubeadmapi.EnvVar{})
			for name, spec := range specs {
				if err := staticpodutil.WriteStaticPodToDisk(name, scratch, spec); err != nil {
					t.Fatalf("kubeadm %s manifest: %v", name, err)
				}
			}

			for _, f := range ours {
				name := filepath.Base(f.Path)
				wantBytes, err := os.ReadFile(filepath.Join(scratch, name))
				if err != nil {
					t.Fatalf("read kubeadm-generated %s: %v", name, err)
				}

				gotObj, err := kubeadmutil.UniversalUnmarshal(f.Content)
				if err != nil {
					t.Fatalf("unmarshal ours %s: %v", name, err)
				}
				wantObj, err := kubeadmutil.UniversalUnmarshal(wantBytes)
				if err != nil {
					t.Fatalf("unmarshal kubeadm %s: %v", name, err)
				}
				gotPod, ok := gotObj.(*corev1.Pod)
				if !ok {
					t.Fatalf("ours %s: not a Pod", name)
				}
				wantPod, ok := wantObj.(*corev1.Pod)
				if !ok {
					t.Fatalf("kubeadm %s: not a Pod", name)
				}
				stripAmbientForParity(wantPod)

				gotYAML, err := kubeadmutil.MarshalToYaml(gotPod, corev1.SchemeGroupVersion)
				if err != nil {
					t.Fatalf("remarshal ours %s: %v", name, err)
				}
				wantYAML, err := kubeadmutil.MarshalToYaml(wantPod, corev1.SchemeGroupVersion)
				if err != nil {
					t.Fatalf("remarshal kubeadm %s: %v", name, err)
				}
				if !bytes.Equal(gotYAML, wantYAML) {
					t.Errorf("%s: parity mismatch against live kubeadm output (ambient CA-trust mounts already stripped)\n--- ours ---\n%s\n--- kubeadm ---\n%s",
						name, gotYAML, wantYAML)
				}
			}
		})
	}
}
