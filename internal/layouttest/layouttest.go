// Package layouttest provides a layout.Layout rooted at t.TempDir() for
// use from *_test.go files. Kept in a separate package from layout
// because importing testing in a non-test .go file would pull
// testing.init()'s flag.CommandLine registrations into the production
// nanokube binary (visible as bogus -test.* flags on the CLI).
package layouttest

import (
	"testing"

	"github.com/MatchaScript/nanokube/internal/layout"
)

// New builds a layout.Layout whose every path lives under t.TempDir().
// Safe to call from t.Parallel() tests — each call returns an
// independent Layout value with no shared mutable state.
func New(t testing.TB) layout.Layout {
	t.Helper()
	return layout.Rooted(t.TempDir())
}
