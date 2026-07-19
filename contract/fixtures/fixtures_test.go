package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kubeadmconfig "k8s.io/kubernetes/cmd/kubeadm/app/util/config"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/MatchaScript/nanokube/contract/desiredpb"
	"github.com/MatchaScript/nanokube/internal/render"
)

// TestFixture_RawMatchesMetadata is the core drift check: it needs
// neither systemd-repart nor mkfs.erofs, so it runs on any host. It
// catches a <name>.raw regenerated without its <name>.json (or vice
// versa), and catches internal/render's rendering or naming logic
// drifting out from under a committed fixture.
func TestFixture_RawMatchesMetadata(t *testing.T) {
	matches, err := filepath.Glob("*.json")
	if err != nil {
		t.Fatalf("glob *.json: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no fixture *.json found in contract/fixtures/ — expected at least one committed fixture")
	}

	cfg, err := kubeadmconfig.DefaultedStaticInitConfiguration()
	if err != nil {
		t.Fatalf("DefaultedStaticInitConfiguration: %v", err)
	}
	kubeletFile, err := render.KubeletConfig(cfg)
	if err != nil {
		t.Fatalf("render.KubeletConfig: %v", err)
	}
	desired := render.Desired{
		Files: []render.File{kubeletFile},
	}
	wantName := desired.Name()

	for _, jsonPath := range matches {
		name := strings.TrimSuffix(filepath.Base(jsonPath), ".json")

		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(jsonPath)
			if err != nil {
				t.Fatalf("read %s: %v", jsonPath, err)
			}
			meta := &desiredpb.DesiredMetadata{}
			if err := protojson.Unmarshal(data, meta); err != nil {
				t.Fatalf("protojson.Unmarshal %s: %v", jsonPath, err)
			}

			if meta.GetName() != name {
				t.Errorf("metadata name = %q, want %q (from filename)", meta.GetName(), name)
			}

			rawPath := filepath.Join(filepath.Dir(jsonPath), name+".raw")
			raw, err := os.ReadFile(rawPath)
			if err != nil {
				t.Fatalf("read sibling %s: %v", rawPath, err)
			}
			sum := sha256.Sum256(raw)
			if got, want := hex.EncodeToString(sum[:]), meta.GetBlobSha256(); got != want {
				t.Errorf("sha256(%s) = %q, want %q (metadata blob_sha256)", rawPath, got, want)
			}

			if name != wantName {
				t.Errorf("fixture name = %q, want %q (current render.Desired.Name() for the canonical fixture input — regenerate with `go run ./contract/fixtures/gen` if internal/render changed)", name, wantName)
			}
		})
	}
}
