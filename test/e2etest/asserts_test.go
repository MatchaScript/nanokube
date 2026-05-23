package e2etest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// recordingTB is a testing.TB stand-in that records Fatalf calls
// instead of aborting the test. Only the methods AssertContains /
// AssertFilePresent / AssertFileAbsent actually call (Helper, Fatalf)
// are exercised here — calls to other TB methods would panic on the
// nil embedded interface, which is fine for these focused tests.
type recordingTB struct {
	testing.TB
	fatals []string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func TestAssertContains_Match(t *testing.T) {
	r := &recordingTB{}
	AssertContains(r, "hello world", "world", "greeting")
	if len(r.fatals) != 0 {
		t.Errorf("unexpected fatals: %v", r.fatals)
	}
}

func TestAssertContains_NoMatch(t *testing.T) {
	r := &recordingTB{}
	AssertContains(r, "hello world", "xyz", "greeting")
	if len(r.fatals) != 1 {
		t.Fatalf("want 1 fatal, got %d: %v", len(r.fatals), r.fatals)
	}
	if want := "greeting"; !contains(r.fatals[0], want) {
		t.Errorf("fatal=%q, want substring %q", r.fatals[0], want)
	}
}

func TestAssertFilePresent_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recordingTB{}
	AssertFilePresent(r, path, "fixture")
	if len(r.fatals) != 0 {
		t.Errorf("unexpected fatals: %v", r.fatals)
	}
}

func TestAssertFilePresent_Missing(t *testing.T) {
	r := &recordingTB{}
	AssertFilePresent(r, "/nonexistent/path/xyz", "fixture")
	if len(r.fatals) != 1 {
		t.Fatalf("want 1 fatal, got %v", r.fatals)
	}
}

func TestAssertFileAbsent_Missing(t *testing.T) {
	r := &recordingTB{}
	AssertFileAbsent(r, "/nonexistent/path/xyz", "fixture")
	if len(r.fatals) != 0 {
		t.Errorf("unexpected fatals: %v", r.fatals)
	}
}

func TestAssertFileAbsent_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stray")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &recordingTB{}
	AssertFileAbsent(r, path, "fixture")
	if len(r.fatals) != 1 {
		t.Fatalf("want 1 fatal, got %v", r.fatals)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
