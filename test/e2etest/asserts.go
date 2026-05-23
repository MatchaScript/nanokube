package e2etest

import (
	"os"
	"strings"
	"testing"
)

// AssertFilePresent fails the test if path does not exist. what is
// included in the failure message for context.
func AssertFilePresent(t testing.TB, path, what string) {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return
	}
	if os.IsNotExist(err) {
		t.Fatalf("%s: %s missing", what, path)
		return
	}
	t.Fatalf("%s: stat %s: %v", what, path, err)
}

// AssertFileAbsent fails the test if path exists.
func AssertFileAbsent(t testing.TB, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s: %s still exists", what, path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("%s: stat %s: %v", what, path, err)
	}
}

// AssertContains fails the test if haystack does not contain needle.
func AssertContains(t testing.TB, haystack, needle, what string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s: output did not contain %q\ngot: %s", what, needle, haystack)
	}
}
