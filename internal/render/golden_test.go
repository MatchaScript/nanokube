package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden regenerates internal/render/testdata/manifests from
// whatever ControlPlaneManifests currently produces. Development-time
// tool only: run `go test ./internal/render/ -run Golden -update` after
// deliberately changing rendered manifest bytes, then inspect the diff
// before committing the regenerated files — this flag is not a way to
// make a failing test pass without understanding why.
var updateGolden = flag.Bool("update", false, "update internal/render/testdata/manifests golden files")

// TestControlPlaneManifests_Golden pins ControlPlaneManifests' output
// byte-for-byte against internal/render/testdata/manifests for the
// config matrix in manifestVariants. See IMPLEMENTATION_PLAN.md §1.3:
// "テンプレート初版の正しさは kubeadm の生成物との突き合わせで確立する" —
// the golden files were captured from kubeadm's own phase-function output
// (the implementation ControlPlaneManifests had before Step 3 T1) and are
// the anchor T1's self-authored construction must reproduce exactly.
func TestControlPlaneManifests_Golden(t *testing.T) {
	for _, v := range manifestVariants() {
		t.Run(v.name, func(t *testing.T) {
			cfg := variantConfig(t, v)
			files, err := ControlPlaneManifests(cfg)
			if err != nil {
				t.Fatalf("ControlPlaneManifests: %v", err)
			}
			if len(files) != 4 {
				t.Fatalf("got %d files, want 4", len(files))
			}

			dir := filepath.Join("testdata", "manifests", v.name)
			for _, f := range files {
				name := filepath.Base(f.Path)
				golden := filepath.Join(dir, name)

				if *updateGolden {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, f.Content, 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}

				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read golden %s: %v (run with -update to generate)", golden, err)
				}
				if !bytes.Equal(f.Content, want) {
					t.Errorf("%s: rendered bytes differ from golden %s\n--- got ---\n%s\n--- want ---\n%s",
						f.Path, golden, f.Content, want)
				}
			}
		})
	}
}

// TestControlPlaneManifests_GoldenDeterministic renders the full variant
// matrix twice in-process and requires identical bytes each time
// (IMPLEMENTATION_PLAN.md §1.3: "実行環境によって出力バイト列が変化する関数は
// すべて排除する").
func TestControlPlaneManifests_GoldenDeterministic(t *testing.T) {
	for _, v := range manifestVariants() {
		t.Run(v.name, func(t *testing.T) {
			cfg := variantConfig(t, v)
			a, err := ControlPlaneManifests(cfg)
			if err != nil {
				t.Fatal(err)
			}
			b, err := ControlPlaneManifests(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(a) != len(b) {
				t.Fatalf("file count differs: %d vs %d", len(a), len(b))
			}
			for i := range a {
				if a[i].Path != b[i].Path || a[i].Mode != b[i].Mode || !bytes.Equal(a[i].Content, b[i].Content) {
					t.Errorf("%s: two renders of the same cfg differ", a[i].Path)
				}
			}
		})
	}
}
