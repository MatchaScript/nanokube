// Command gen regenerates the golden fixture pair in contract/fixtures/
// for the desired-document cross-language contract: a canonical
// render.Desired, built into a confext DDI (<name>.raw) by
// internal/ddi, plus its sidecar metadata (<name>.json).
//
// render.KubeletConfig is not a pure function of its
// *kubeadmapi.InitConfiguration argument alone: kubeadm's own
// componentconfig defaulting also probes the local init system (is
// systemd-resolved active on THIS machine?) and bakes the answer into
// the rendered kubelet-config.yaml's resolvConf field. For the
// committed fixture to agree with fixtures_test.go — which re-derives
// the same Desired natively, on whatever host later runs `go test` —
// the render step must run in that same kind of environment (a real
// host, not a bare build container with no systemd running). Building
// the confext DDI blob itself is a separate concern: it shells out to
// systemd-repart, which needs mkfs.erofs, typically only available
// inside a container on a host like this repo's dev box.
//
// gen is therefore split into two subcommands so each half can run
// where it needs to, connected by a manifest.json + staged files
// directory (the "manifest dir") that the build subcommand reads back
// verbatim, without ever re-invoking render.KubeletConfig itself:
//
//	mise exec -- go run ./contract/fixtures/gen render <manifest-dir>
//
//	# then, inside a container with systemd-repart + mkfs.erofs on
//	# PATH (building the DDI is unprivileged):
//	<gen-binary> build <manifest-dir>
//
// Run from the repo root; build writes into contract/fixtures/.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/MatchaScript/nanokube/contract/desiredpb"
	"github.com/MatchaScript/nanokube/internal/ddi"
	"github.com/MatchaScript/nanokube/internal/render"
)

// fixturesDir is repo-root relative; the build subcommand must be run
// from the repo root.
const fixturesDir = "contract/fixtures"

// manifest is gen's own private handoff format between its render and
// build subcommands. Files are staged as real files on disk under the
// manifest directory (path relative to it, mirroring each render.File.Path)
// so build can reconstruct []render.File byte-for-byte without
// re-rendering.
type manifest struct {
	Files []manifestFileMeta `json:"files"`
}

type manifestFileMeta struct {
	Path string      `json:"path"`
	Mode os.FileMode `json:"mode"`
}

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("usage: %s render|build <manifest-dir>", os.Args[0])
	}
	manifestDir := os.Args[2]

	switch os.Args[1] {
	case "render":
		runRender(manifestDir)
	case "build":
		runBuild(manifestDir)
	default:
		log.Fatalf("usage: %s render|build <manifest-dir>", os.Args[0])
	}
}

// runRender renders the canonical fixture input — the same way
// fixtures_test.go independently re-derives it — and stages the
// result under manifestDir for a later runBuild (possibly in a
// different environment) to pick up unchanged.
func runRender(manifestDir string) {
	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		log.Fatalf("gen render: DefaultedStaticInitConfiguration: %v", err)
	}
	kubeletFile, err := render.KubeletConfig(cfg)
	if err != nil {
		log.Fatalf("gen render: render.KubeletConfig: %v", err)
	}
	files := []render.File{kubeletFile}

	var m manifest
	for _, f := range files {
		dest := filepath.Join(manifestDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			log.Fatalf("gen render: mkdir for %s: %v", f.Path, err)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dest, f.Content, mode); err != nil {
			log.Fatalf("gen render: write %s: %v", dest, err)
		}
		m.Files = append(m.Files, manifestFileMeta{Path: f.Path, Mode: mode})
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatalf("gen render: marshal manifest: %v", err)
	}
	manifestPath := filepath.Join(manifestDir, "manifest.json")
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		log.Fatalf("gen render: write %s: %v", manifestPath, err)
	}

	desired := render.Desired{Files: files}
	fmt.Printf("rendered %d file(s) into %s (name will be %s)\n", len(files), manifestDir, desired.Name())
}

// runBuild reads back manifestDir's staged files (written by a prior
// runRender, possibly on a different host) and builds the confext DDI
// fixture pair from them, unchanged.
func runBuild(manifestDir string) {
	manifestPath := filepath.Join(manifestDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		log.Fatalf("gen build: read %s: %v", manifestPath, err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		log.Fatalf("gen build: unmarshal %s: %v", manifestPath, err)
	}

	files := make([]render.File, 0, len(m.Files))
	for _, fm := range m.Files {
		content, err := os.ReadFile(filepath.Join(manifestDir, fm.Path))
		if err != nil {
			log.Fatalf("gen build: read staged %s: %v", fm.Path, err)
		}
		files = append(files, render.File{Path: fm.Path, Content: content, Mode: fm.Mode})
	}

	desired := render.Desired{Files: files}
	name := desired.Name()

	rawPath := filepath.Join(fixturesDir, name+".raw")
	if err := ddi.Build(ddi.BuildInput{
		Name:  name,
		Files: desired.Files,
	}, rawPath); err != nil {
		log.Fatalf("gen build: ddi.Build: %v", err)
	}

	raw, err := os.ReadFile(rawPath)
	if err != nil {
		log.Fatalf("gen build: read back %s: %v", rawPath, err)
	}
	sum := sha256.Sum256(raw)

	meta := &desiredpb.DesiredMetadata{
		Name:       name,
		BlobSha256: hex.EncodeToString(sum[:]),
	}
	jsonData, err := (protojson.MarshalOptions{Multiline: true, Indent: "  "}).Marshal(meta)
	if err != nil {
		log.Fatalf("gen build: protojson.Marshal: %v", err)
	}
	jsonPath := filepath.Join(fixturesDir, name+".json")
	if err := os.WriteFile(jsonPath, jsonData, 0o644); err != nil {
		log.Fatalf("gen build: write %s: %v", jsonPath, err)
	}

	fmt.Printf("generated fixture %s\n  raw:  %s (%d bytes)\n  json: %s (%d bytes)\n", name, rawPath, len(raw), jsonPath, len(jsonData))
}
