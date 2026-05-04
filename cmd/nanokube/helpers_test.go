package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MatchaScript/nanokube/internal/paths"
	"github.com/MatchaScript/nanokube/internal/testutil"
)

// seedStateAsExisting repoints the paths package at t.TempDir() and
// drops a kube-apiserver static pod manifest, the single signal
// state.Exists() now uses to report a prior init.
func seedStateAsExisting(t *testing.T) {
	t.Helper()
	testutil.UseTempPaths(t)
	manifest := filepath.Join(paths.ManifestsDir, "kube-apiserver.yaml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("apiVersion: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
