package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/testutil"
)

func TestReadLastBoot_MissingFileIsNotAnError(t *testing.T) {
	testutil.UseTempPaths(t)

	lb, had, err := ReadLastBoot()
	if err != nil {
		t.Fatalf("ReadLastBoot on empty state: %v", err)
	}
	if had {
		t.Errorf("had = true on empty state")
	}
	if lb != (LastBoot{}) {
		t.Errorf("LastBoot = %+v; want zero value", lb)
	}
}

func TestWriteLastBoot_RoundTripPreservesFields(t *testing.T) {
	testutil.UseTempPaths(t)

	want := LastBoot{
		Version:      "v1.35.0",
		DeploymentID: "abc123def456",
		BootID:       "11112222-3333-4444-5555-666677778888",
	}
	if err := WriteLastBoot(want); err != nil {
		t.Fatalf("WriteLastBoot: %v", err)
	}

	got, had, err := ReadLastBoot()
	if err != nil || !had {
		t.Fatalf("ReadLastBoot: err=%v had=%v", err, had)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestWriteLastBoot_CreatesStateDir(t *testing.T) {
	testutil.UseTempPaths(t)
	if _, err := os.Stat(paths.StateDir); !os.IsNotExist(err) {
		t.Fatalf("pre-condition: StateDir must not exist yet; got %v", err)
	}
	if err := WriteLastBoot(LastBoot{Version: "v1.0"}); err != nil {
		t.Fatalf("WriteLastBoot: %v", err)
	}
	if _, err := os.Stat(paths.StateDir); err != nil {
		t.Errorf("StateDir not created: %v", err)
	}
}

func TestWriteLastBoot_IsAtomic(t *testing.T) {
	testutil.UseTempPaths(t)

	if err := WriteLastBoot(LastBoot{Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLastBoot(LastBoot{Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := ReadLastBoot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v2" {
		t.Errorf("last write lost: got %q", got.Version)
	}
	// No stray .tmp-* leftover in StateDir.
	entries, err := os.ReadDir(paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestReadLastBoot_CorruptedFileBubbles(t *testing.T) {
	testutil.UseTempPaths(t)
	if err := os.MkdirAll(paths.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.LastBootFile, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadLastBoot()
	if err == nil {
		t.Fatal("ReadLastBoot on corrupt file = nil")
	}
}

func TestWriteLastEvent_AppendsNewline(t *testing.T) {
	testutil.UseTempPaths(t)

	if err := WriteLastEvent("hello world"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths.LastEventFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello world\n" {
		t.Errorf("event file = %q; want %q", string(raw), "hello world\n")
	}

	got, err := ReadLastEvent()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("ReadLastEvent trimmed to %q", got)
	}
}

func TestReadLastEvent_EmptyWhenAbsent(t *testing.T) {
	testutil.UseTempPaths(t)
	got, err := ReadLastEvent()
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("ReadLastEvent on empty state = %q; want empty", got)
	}
}

// Exists() trips on either the kube-apiserver static pod manifest
// (init done) OR the /var/lib/nanokube tree (lifecycle data left over).
// Cover each independently so a future refactor dropping one fails the
// matching subtest.
func TestExists_TwoIndependentSignals(t *testing.T) {
	t.Run("nothing exists", func(t *testing.T) {
		testutil.UseTempPaths(t)
		got, err := Exists()
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Fatal("Exists() = true on empty state")
		}
	})

	t.Run("manifest alone trips", func(t *testing.T) {
		testutil.UseTempPaths(t)
		manifest := filepath.Join(paths.ManifestsDir, "kube-apiserver.yaml")
		if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, []byte("apiVersion: v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Exists()
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatal("Exists() = false; manifest alone must trip")
		}
	})

	t.Run("var-lib-nanokube alone trips", func(t *testing.T) {
		testutil.UseTempPaths(t)
		// Anything under NanoKubeVarDir creates the dir; last-boot.json
		// is the realistic case (lifecycle wrote it then someone wiped
		// /etc/kubernetes manually).
		if err := WriteLastBoot(LastBoot{Version: "v1"}); err != nil {
			t.Fatal(err)
		}
		got, err := Exists()
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatal("Exists() = false; /var/lib/nanokube alone must trip")
		}
	})
}
