package atomic

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSwapDir_Exchange(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	live := filepath.Join(root, "live")
	mustMkdir(t, stage)
	mustMkdir(t, live)
	mustWrite(t, filepath.Join(stage, "new"), "new")
	mustWrite(t, filepath.Join(live, "old"), "old")

	if err := SwapDir(stage, live); err != nil {
		t.Fatalf("SwapDir: %v", err)
	}

	if got := mustRead(t, filepath.Join(live, "new")); got != "new" {
		t.Errorf("live/new = %q, want new", got)
	}
	if got := mustRead(t, filepath.Join(stage, "old")); got != "old" {
		t.Errorf("stage/old = %q, want old (stage holds displaced live contents)", got)
	}
}

func TestSwapDir_PlainRenameWhenLiveAbsent(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	live := filepath.Join(root, "live") // does not exist
	mustMkdir(t, stage)
	mustWrite(t, filepath.Join(stage, "new"), "new")

	if err := SwapDir(stage, live); err != nil {
		t.Fatalf("SwapDir: %v", err)
	}

	if got := mustRead(t, filepath.Join(live, "new")); got != "new" {
		t.Errorf("live/new = %q, want new", got)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stage still exists after plain rename: err=%v", err)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
